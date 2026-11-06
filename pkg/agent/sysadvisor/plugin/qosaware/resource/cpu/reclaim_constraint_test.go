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

package cpu

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/cputypes"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
)

var (
	testGlobalScope = provisionassembler.NewNonExclusiveReclaimConstraintScope(-1)
	testNUMAScope0  = provisionassembler.NewNonExclusiveReclaimConstraintScope(0)
	testNUMAScope1  = provisionassembler.NewNonExclusiveReclaimConstraintScope(1)
)

func constrainTestTarget(g *reclaimConstraintGuard, scope provisionassembler.ReclaimConstraintScope, desired, floor, steadyCap int) {
	if g.targets == nil {
		g.targets = make(map[provisionassembler.ReclaimConstraintScope]types.ReclaimConstraintTarget)
	}
	g.targets[scope] = types.ReclaimConstraintTarget{
		Desired:     desired,
		Floor:       floor,
		SteadyCap:   steadyCap,
		MemberNUMAs: []int{0},
	}
}

func ceilingVal(ceilings map[provisionassembler.ReclaimConstraintScope]*int, scope provisionassembler.ReclaimConstraintScope) (int, bool) {
	c, ok := ceilings[scope]
	if !ok || c == nil {
		return 0, false
	}
	return *c, true
}

func runCycle(
	g *reclaimConstraintGuard,
	active map[provisionassembler.ReclaimConstraintScope]bool,
	observedByScope map[provisionassembler.ReclaimConstraintScope]int,
	observedOKByScope map[provisionassembler.ReclaimConstraintScope]bool,
	step, cpusPerCore int,
) (cputypes.ReclaimConstraint, map[provisionassembler.ReclaimConstraintScope]*int) {
	constraint, ceilings, _, accounting := g.constraint(active, observedByScope, observedOKByScope, step, cpusPerCore)

	publishedBy := map[provisionassembler.ReclaimConstraintScope]int{}
	scopeNumas := map[provisionassembler.ReclaimConstraintScope][]int{}
	publish := func(scope provisionassembler.ReclaimConstraintScope) {
		if v, ok := ceilingVal(ceilings, scope); ok {
			publishedBy[scope] = v
		} else if t, ok := g.targets[scope]; ok {
			publishedBy[scope] = t.SteadyCap
		}
		scopeNumas[scope] = []int{0}
	}
	for scope := range active {
		publish(scope)
	}
	for scope := range g.scopeState {
		if _, ok := active[scope]; !ok {
			publish(scope)
		}
	}
	g.commit(active, ceilings, accounting, nil, publishedBy, scopeNumas)
	return constraint, ceilings
}

// runCycleAborted mirrors an update cycle that fails (e.g. an isolation
// safety-check rollback) after deciding but before commit(): it runs the pure
// decision and deliberately discards the result, so the retained guard state
// must be byte-for-byte unchanged. The next successful cycle then re-decides from
// the untouched baseline instead of seeing a half-applied ceiling or a corrupted
// ACK counter.
func runCycleAborted(
	g *reclaimConstraintGuard,
	active map[provisionassembler.ReclaimConstraintScope]bool,
	observedByScope map[provisionassembler.ReclaimConstraintScope]int,
	observedOKByScope map[provisionassembler.ReclaimConstraintScope]bool,
	step, cpusPerCore int,
) (cputypes.ReclaimConstraint, map[provisionassembler.ReclaimConstraintScope]*int) {
	c, ceilings, _, _ := g.constraint(active, observedByScope, observedOKByScope, step, cpusPerCore)
	return c, ceilings
}

func TestGlobalRampUpStepsDownTowardDesired(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	c, ceilings := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
	require.Equal(t, cputypes.ReclaimConstraintReservedFloor, c)
	v, _ := ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 36, v, "first activation seeds from observed, held")

	for i, want := range []int{30, 24, 24} {
		_, ceilings = runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: v}, obsOK, 6, 2)
		v, _ = ceilingVal(ceilings, testGlobalScope)
		require.Equal(t, want, v, "cycle %d", i+2)
	}
}

func TestDrainingStepsCeilingBackToSteady(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	pub := 36
	for _, want := range []int{36, 30, 24} {
		_, ceilings := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: pub}, obsOK, 6, 2)
		pub, _ = ceilingVal(ceilings, testGlobalScope)
		require.Equal(t, want, pub)
	}

	noActive := map[provisionassembler.ReclaimConstraintScope]bool{}
	c, ceilings := runCycle(g, noActive, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: pub}, obsOK, 6, 2)
	require.Equal(t, cputypes.ReclaimConstraintReservedFloor, c)
	pub, _ = ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 30, pub, "draining steps up by step")

	_, ceilings = runCycle(g, noActive, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: pub}, obsOK, 6, 2)
	pub, _ = ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 36, pub, "draining reaches steadyCap")

	c, _ = runCycle(g, noActive, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: pub}, obsOK, 6, 2)
	require.Equal(t, cputypes.ReclaimConstraintNone, c, "retired scope stops constraining")
}

func TestFirstActivationSeedsFromObservedNotZero(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	c, ceilings, _, _ := g.constraint(active,
		map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36},
		map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}, 6, 2)
	require.Equal(t, cputypes.ReclaimConstraintReservedFloor, c)
	v, ok := ceilingVal(ceilings, testGlobalScope)
	require.True(t, ok)
	require.Equal(t, 36, v, "seeded from observed 36, not from 0")
}

func TestFirstActivationNoObservationIsUnconstrained(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	c, ceilings, _, _ := g.constraint(active,
		map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 0},
		map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: false}, 6, 2)
	require.Equal(t, cputypes.ReclaimConstraintReservedFloor, c)
	_, ok := ceilingVal(ceilings, testGlobalScope)
	require.False(t, ok, "no observation => nil/unconstrained ceiling")
}

func TestScopeIsolation(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	constrainTestTarget(g, testNUMAScope0, 12, 5, 18)

	active := map[provisionassembler.ReclaimConstraintScope]bool{testNUMAScope0: true}
	runCycle(g, active,
		map[provisionassembler.ReclaimConstraintScope]int{testNUMAScope0: 18},
		map[provisionassembler.ReclaimConstraintScope]bool{testNUMAScope0: true}, 6, 2)

	_, hasGlobal := g.scopeState[testGlobalScope]
	require.False(t, hasGlobal, "global scope untouched by SNB activation")
	_, hasSNB := g.scopeState[testNUMAScope0]
	require.True(t, hasSNB)
}

func TestACKHoldFreezesCeiling(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)

	c, ceilings, _, _ := g.constraint(active,
		map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 35},
		map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}, 6, 2)
	require.Equal(t, cputypes.ReclaimConstraintReservedFloor, c)
	v, _ := ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 36, v, "un-acked cycle freezes ceiling at 36")
}

func TestMultiScopeIndependentACK(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testNUMAScope0, 12, 5, 18)
	constrainTestTarget(g, testNUMAScope1, 8, 4, 12)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testNUMAScope0: true, testNUMAScope1: true}
	obs := map[provisionassembler.ReclaimConstraintScope]int{testNUMAScope0: 18, testNUMAScope1: 12}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testNUMAScope0: true, testNUMAScope1: true}

	_, ceilings := runCycle(g, active, obs, obsOK, 4, 2)
	v0, _ := ceilingVal(ceilings, testNUMAScope0)
	v1, _ := ceilingVal(ceilings, testNUMAScope1)
	require.Equal(t, 18, v0)
	require.Equal(t, 12, v1)

	_, ceilings = runCycle(g, active, obs, obsOK, 4, 2)
	v0, _ = ceilingVal(ceilings, testNUMAScope0)
	v1, _ = ceilingVal(ceilings, testNUMAScope1)
	require.Equal(t, 14, v0, "scope0 steps down by 4")
	require.Equal(t, 8, v1, "scope1 reaches its desired 8")
}

func TestReactivationReusesRetainedCeiling(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	// step=2, cpusPerCore=1 so a drain takes several cycles and the scope stays
	// retained (never reaches steadyCap=36 in one step).
	pub := 36
	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: pub}, obsOK, 2, 1)
	_, ceilings := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 2, 1)
	pub, _ = ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 34, pub)

	// Ramp-up flaps off for one draining cycle. ceiling 34 -> 36? No: step=2, so
	// 34+2=36 == steadyCap and it would retire. Use one more active step down first
	// so the retained ceiling sits at 32 and draining moves only to 34.
	_, ceilings = runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 34}, obsOK, 2, 1)
	pub, _ = ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 32, pub)

	noActive := map[provisionassembler.ReclaimConstraintScope]bool{}
	_, ceilings = runCycle(g, noActive, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: pub}, obsOK, 2, 1)
	pub, _ = ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 34, pub, "draining steps up one step, still below steadyCap so retained")

	// Ramp-up flaps back on. The scope resumes from the retained 34 (not re-seeded
	// from observed 34 and certainly not from 0). Because the observation ACKs, it
	// immediately steps back down one step toward desired=24. The invariant we guard
	// is that the ceiling starts from the retained 34 and decrements by exactly one
	// step (32), rather than re-seeding at 36/0 and jumping.
	_, ceilings = runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 34}, obsOK, 2, 1)
	pub, _ = ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 32, pub, "resumed from retained 34 and stepped down one step, no re-seed jump")
}

// TestQuasiACKToleranceReseedsAfterGraceCycles covers the within-tolerance ACK:
// when the observed value sits within cpusPerCore of lastPublished for
// quasiAckGraceCycles consecutive cycles, the guard treats it as an ACK and
// advances the ceiling.
func TestQuasiACKToleranceReseedsAfterGraceCycles(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	// Seed at observed=36.
	pub := 36
	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: pub}, obsOK, 6, 2)
	// First ACK: exact equality -> step to 30.
	_, ceilings := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
	pub, _ = ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 30, pub)

	// Observed lags by 2 (<= cpusPerCore=2). For the first (quasiAckGraceCycles-1)
	// cycles the ceiling must stay frozen at 30; only after the grace window does
	// it quasi-ACK and step down.
	for i := 0; i < quasiAckGraceCycles-1; i++ {
		_, ceilings = runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 28}, obsOK, 6, 2)
		pub, _ = ceilingVal(ceilings, testGlobalScope)
		require.Equal(t, 30, pub, "cycle %d: ceiling frozen during grace window", i+1)
	}
	// Third consecutive within-tolerance observation -> quasi ACK, step down.
	_, ceilings = runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 28}, obsOK, 6, 2)
	pub, _ = ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 24, pub, "quasi-ACK after grace window advances to desired")
}

// TestRestartSeedsFromObservedNotZero simulates a guard rebuilt without
// checkpoint: the first activation must seed from the observed pool size, never
// from 0.
func TestRestartSeedsFromObservedNotZero(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	_, ceilings := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 33}, obsOK, 6, 2)
	pub, ok := ceilingVal(ceilings, testGlobalScope)
	require.True(t, ok)
	require.Equal(t, 33, pub, "restart seeds from observed pool, not 0 and not floor")
}

// TestMembershipChangeRecomputesAndRateLimits verifies that changing the member
// NUMA set does not reset the ceiling to the observed steady value; the ceiling
// keeps tracking its desired at one step per cycle.
func TestMembershipChangeRecomputesAndRateLimits(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
	_, ceilings := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
	pub, _ := ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 30, pub)

	// Descriptor recompute: shrink desired/floor/member set; ceiling must keep
	// tracking the new desired at one step, not jump.
	g.targets[testGlobalScope] = types.ReclaimConstraintTarget{Desired: 18, Floor: 10, SteadyCap: 36, MemberNUMAs: []int{0, 1}}
	_, ceilings = runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 30}, obsOK, 6, 2)
	pub, _ = ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 24, pub, "ceiling steps one step toward new desired after member change")
}

// TestDesiredCappedAtSteadyCap pins the case where the raw ramp-up target
// exceeds SteadyCap: desired collapses to SteadyCap and the floor is never
// lifted, so floor <= pool <= steadyCap holds.
func TestDesiredCappedAtSteadyCap(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{
		scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{},
		targets:    map[provisionassembler.ReclaimConstraintScope]types.ReclaimConstraintTarget{},
	}
	// initialRatio > MaxRatio upstream resolves desired to SteadyCap; the guard
	// trusts that descriptor and never lifts the floor (10) above desired.
	g.targets[testGlobalScope] = types.ReclaimConstraintTarget{Desired: 36, Floor: 10, SteadyCap: 36, MemberNUMAs: []int{0}}
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	// Seed at observed 36 (= steadyCap).
	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
	_, ceilings := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
	pub, _ := ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 36, pub, "desired never exceeds steadyCap; pool pinned at steadyCap")
}

// TestNoObservationNeverDropsPoolToZero verifies that with no observation evidence
// the ceiling stays nil (unconstrained) rather than collapsing the pool to 0.
func TestNoObservationNeverDropsPoolToZero(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: false}

	_, ceilings := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{}, obsOK, 6, 2)
	_, ok := ceilingVal(ceilings, testGlobalScope)
	require.False(t, ok, "nil ceiling = unconstrained; pool keeps steady size, never floored to 0")
}

// TestActiveNoDescriptorKeepsSteadySize guards the missing-descriptor fail-closed invariant: an active scope
// whose ramp-up domain descriptor never resolves must stay unconstrained. The
// pool keeps its steady size and must NOT ratchet down toward a zero-value
// desired after a (non-existent) first ACK.
func TestActiveNoDescriptorKeepsSteadySize(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	for i := 0; i < 3; i++ {
		_, ceilings := runCycle(g, active,
			map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
		_, has := ceilingVal(ceilings, testGlobalScope)
		require.False(t, has, "cycle %d: no descriptor => nil/unconstrained ceiling, pool stays steady", i+1)
	}
	state, ok := g.scopeState[testGlobalScope]
	require.True(t, ok, "an active scope is tracked even without a descriptor")
	require.False(t, state.HasDescriptor, "descriptor never arrived")
	require.False(t, state.HasPublished, "no publication => no fabricated ACK baseline")
	require.Nil(t, state.Ceiling, "retained ceiling stays nil")
	require.Zero(t, state.StallCycles, "no active-vs-desired stall when unconstrained")
}

// TestAbortedCycleLeavesStateUntouched guards the decision/commit separation: a cycle that decides
// but does not commit (update fails before publication) must not mutate the
// retained scope state -- no half-advanced ceiling and no corrupted ACK counter.
func TestAbortedCycleLeavesStateUntouched(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	// One successful cycle seeds and publishes.
	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
	before := g.scopeState[testGlobalScope]

	// An aborted cycle: decide (and even observe a new value) but do not commit.
	runCycleAborted(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 30}, obsOK, 6, 2)
	after := g.scopeState[testGlobalScope]

	require.Equal(t, before, after, "aborted cycle must leave retained state untouched")
}

// TestStaleObservationIsNotACK guards the ACK protocol: an observed value that
// equals an EARLIER published value but not the last one must not count as an
// ACK and must not advance the ceiling.
func TestStaleObservationIsNotACK(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	// Seed 36, then ACK to step down to 30 (LastPublished=30).
	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)

	// Observe 24 -- an old/future value, not equal to LastPublished=30 and outside
	// the cpusPerCore=2 tolerance. It must NOT ACK; the ceiling freezes at 30.
	c, ceilings, _, _ := g.constraint(active,
		map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 24}, obsOK, 6, 2)
	require.Equal(t, cputypes.ReclaimConstraintReservedFloor, c)
	v, _ := ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 30, v, "stale observation outside tolerance must not advance the ceiling")
}

// TestMissingPoolIsNotACK guards the missing-evidence invariant: a scope with no live pool
// observation (observedOK=false) is not treated as an ACK.
func TestMissingPoolIsNotACK(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	// Seed from an observed pool.
	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36},
		map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}, 6, 2)

	// Next cycle the pool vanishes: observedOK=false. The ceiling must stay put
	// (un-acknowledged), not advance and not ratchet.
	c, ceilings, _, _ := g.constraint(active,
		map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 0},
		map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: false}, 6, 2)
	require.Equal(t, cputypes.ReclaimConstraintReservedFloor, c)
	v, _ := ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 36, v, "missing pool observation freezes the ceiling; no ACK")
}

// TestNonPositiveStepFailsClosed guards the misconfiguration fail-closed path: a non-positive per-cycle
// rate publishes no dynamic ceiling and leaves the retained ceiling intact, so a
// transient misconfiguration neither yanks the pool down to a floor nor wipes the
// anti-flap bookkeeping.
func TestNonPositiveStepFailsClosed(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	// Establish a retained ceiling of 36.
	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
	require.NotNil(t, g.scopeState[testGlobalScope].Ceiling)

	// Now the rate drops to 0. Expect ReservedFloor semantics but a nil ceiling,
	// and the retained ceiling NOT overwritten.
	c, ceilings, constrained, _ := g.constraint(active,
		map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 0, 2)
	require.Equal(t, cputypes.ReclaimConstraintReservedFloor, c)
	require.True(t, constrained[testGlobalScope])
	require.Nil(t, ceilings[testGlobalScope], "non-positive step => nil/unconstrained ceiling")
	require.NotNil(t, g.scopeState[testGlobalScope].Ceiling, "retained ceiling preserved across the bad cycle")
}

// TestDescriptorChangeReguidesCeiling guards descriptor-change re-guidance: when the ramp-up
// descriptor changes (operator retargets) while the scope is still active, the
// guard converges toward the NEW desired rather than leaking the old one.
func TestDescriptorChangeReguidesCeiling(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	// Seed 36, then ACK (observed == published) to step down to 34 with step=2.
	runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 2, 1)
	_, ceilings := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 2, 1)
	pub, _ := ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 34, pub)

	// Operator retargets desired from 24 up to 33. The guard must converge toward
	// the NEW 33: one step (2) from 34 would overshoot to 32, so it clamps to 33.
	// (Had it kept tracking the old desired 24, it would land on 32 instead.)
	g.targets[testGlobalScope] = types.ReclaimConstraintTarget{Desired: 33, Floor: 10, SteadyCap: 36, MemberNUMAs: []int{0}}
	_, ceilings = runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 34}, obsOK, 2, 1)
	pub, _ = ceilingVal(ceilings, testGlobalScope)
	require.Equal(t, 33, pub, "ceiling re-guides toward the new desired 33, not the stale 24")
}

func intPtr(v int) *int { return &v }

// TestDrainingScopeAbsentFromPublishedSetIsRetained pins the P1-1 inversion in
// commit(): a scope still draining its ceiling back to steadyCap must be RETAINED
// when it disappears from this cycle's published scope numbering (its dedicated
// region flapped out of the region map for a cycle). Deleting it there would make
// the next cycle re-seed from the observed pool and yank the ceiling straight back
// up to steadyCap. Only once the scope has retired (ceiling reached steadyCap,
// nothing left to converge) may its absence drop the retained state.
func TestDrainingScopeAbsentFromPublishedSetIsRetained(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{
		scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{
			testGlobalScope: {
				Phase:         reclaimPhaseDraining,
				LastPublished: 30,
				HasPublished:  true,
				HasDescriptor: true,
				Ceiling:       intPtr(30),
				Desired:       36,
				Floor:         10,
				SteadyCap:     36,
				MemberNUMAs:   []int{0},
			},
		},
		targets: map[provisionassembler.ReclaimConstraintScope]types.ReclaimConstraintTarget{
			testGlobalScope: {Desired: 36, Floor: 10, SteadyCap: 36, MemberNUMAs: []int{0}},
		},
	}

	noActive := map[provisionassembler.ReclaimConstraintScope]bool{}
	// This cycle's published scope numbering is EMPTY: the scope's region has
	// dropped out of the region map, even though it is still draining.
	absentScopeNumas := map[provisionassembler.ReclaimConstraintScope][]int{}

	// drainOneCycle advances the retained ceiling by one ACKed step while keeping
	// the scope absent from scopeNumas. It returns the freshly published ceiling.
	drainOneCycle := func(t *testing.T, observed int) int {
		_, ceilings, _, accounting := g.constraint(noActive,
			map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: observed},
			map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}, 2, 1)
		pub, ok := ceilingVal(ceilings, testGlobalScope)
		require.True(t, ok, "draining scope still publishes a ceiling")
		g.commit(noActive, ceilings, accounting, nil,
			map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: pub}, absentScopeNumas)
		return pub
	}

	// Step 1: 30 -> 32, still below steadyCap. The scope MUST be retained even
	// though it is absent from scopeNumas (this is exactly the case the inverted
	// cleanup used to delete).
	pub := drainOneCycle(t, 30)
	require.Equal(t, 32, pub)
	state, stillThere := g.scopeState[testGlobalScope]
	require.True(t, stillThere, "draining scope absent one cycle must be retained, not re-seeded from zero")
	require.NotNil(t, state.Ceiling)
	require.Equal(t, 32, *state.Ceiling, "retained ceiling kept its converging value")

	// Step 2: 32 -> 34, still draining, still absent. Retained again.
	pub = drainOneCycle(t, 32)
	require.Equal(t, 34, pub)
	_, stillThere = g.scopeState[testGlobalScope]
	require.True(t, stillThere, "still draining (34 < steadyCap 36): retained across a second absent cycle")

	// Step 3: 34 -> 36 reaches steadyCap => retired. Now the absence may finally
	// drop the state (it has converged, nothing left to preserve).
	pub = drainOneCycle(t, 34)
	require.Equal(t, 36, pub)
	_, gone := g.scopeState[testGlobalScope]
	require.False(t, gone, "retired scope (ceiling == steadyCap) dropped once absent from the published set")
}

// TestNilCeilingCycleReseedsFromPublishedSize is the recovery/rate-limit regression:
// after a scope converges, a cycle whose per-cycle rate is configured non-positive
// publishes NO ceiling (unconstrained) and the pool runs out to its steady size. On
// recovery the retained ceiling must be anchored to that published unconstrained size,
// so the first recovered ceiling steps back by at most one rate-limit step -- not
// clamp the pool straight from steady back to the stale converged ceiling (a drop of
// more than one step).
func TestNilCeilingCycleReseedsFromPublishedSize(t *testing.T) {
	t.Parallel()
	g := &reclaimConstraintGuard{scopeState: map[provisionassembler.ReclaimConstraintScope]reclaimConstraintScopeState{}}
	constrainTestTarget(g, testGlobalScope, 24, 10, 36)
	active := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}
	obsOK := map[provisionassembler.ReclaimConstraintScope]bool{testGlobalScope: true}

	// Converge the ceiling from observed 36 down to desired 24, one step (6) per
	// acked cycle: 36 -> 30 -> 24 -> 24.
	for _, observed := range []int{36, 36, 30, 24, 24} {
		runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: observed}, obsOK, 6, 2)
	}
	st, ok := g.scopeState[testGlobalScope]
	require.True(t, ok)
	require.NotNil(t, st.Ceiling)
	require.Equal(t, 24, *st.Ceiling, "converged ceiling anchored at desired")
	require.Equal(t, 24, st.LastPublished)

	// Nil-ceiling cycle: rate configured non-positive -> guard publishes no ceiling;
	// the pool runs unconstrained out to steadyCap 36.
	_, nilCeilings := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 0, 2)
	_, hasNil := ceilingVal(nilCeilings, testGlobalScope)
	require.False(t, hasNil, "non-positive rate => nil/unconstrained ceiling this cycle")

	// Recovery: rate returns. The pool is physically at 36. The first recovered
	// ceiling may step down by at most one step (6) => must be >= 30. Without the
	// reseed it clamps straight to the stale 24, a 12-CPU drop in one cycle.
	_, recovered := runCycle(g, active, map[provisionassembler.ReclaimConstraintScope]int{testGlobalScope: 36}, obsOK, 6, 2)
	v, ok := ceilingVal(recovered, testGlobalScope)
	require.True(t, ok)
	require.GreaterOrEqual(t, v, 30,
		"recovery must not drop the pool more than one step from its published unconstrained size 36")
}
