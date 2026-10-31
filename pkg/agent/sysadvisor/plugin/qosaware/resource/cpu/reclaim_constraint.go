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

// Package cpu owns the per-scope reclaim ceiling state machine that slowly
// converges the shared reclaim pool toward the ramp-up target (and back)
// without ever yanking it in a single cycle.
//
// # Four quantities (contract)
//
// A reclaim scope (one non-binding NUMA, the global aggregate, or an exclusive
// dedicated region) carries four distinct quantities. They must never be
// conflated:
//
//   - floor       the pure steady reservation lower bound. The pool is never
//     allowed below it; it is the NumaMinReclaimedResourceForAllocate
//     reserve. It is NEVER raised by ramp-up.
//   - desired     the ramp-up midpoint the ceiling converges toward while the
//     scope is active. After ramp-up exits it reverts to steadyCap.
//   - ceiling     the OPTIONAL, rate-limited upper bound the guard publishes to
//     the assembler. nil means "unconstrained": the pool keeps its
//     steady size and must not be floored to an arbitrary value.
//     The ceiling moves by at most one whole-core step per cycle and
//     only after QRM acknowledges the previously published value.
//   - steadyCap   the pool upper bound when no ramp-up is active (the MaxRatio
//     cap). After ramp-up exits the ceiling steps back up to it.
//
// Invariant: floor <= pool <= steadyCap; desired lives in [floor, steadyCap].
// MaxRatio only ever caps; it never raises the floor.
//
// # ACK protocol
//
// The guard does not trust that QRM applied the published ceiling. It compares
// the observed pool size against the last published value:
//
//   - exact equality       is a precise ACK: the ceiling may advance one step.
//   - within cpusPerCore    for quasiAckGraceCycles consecutive cycles is a
//     tolerant (quasi) ACK: QRM is close enough that the
//     pool is effectively tracking; the ceiling may then
//     advance. A larger deviation resets the grace window
//     rather than accumulating it.
//   - anything else         the ceiling is held frozen and the stall counter is
//     incremented.
//
// There is deliberately NO wall-clock timeout on the ACK. A missing/late ACK is
// fail-closed: the ceiling stays frozen (the pool keeps its last published
// size) rather than being advanced on a hunch and risking an over-shrink. The
// only escape is an exact or tolerated observation, which is exactly the
// back-pressure we want when QRM cannot keep up.
//
// # Decision vs commit
//
// constraint()/decideCeiling() are pure: they read the retained state and
// return this cycle's ceilings together with the ACK bookkeeping (whether the
// cycle acked, the proposed quasi-grace counter, and the stall increment). They
// never mutate g.scopeState. commit() is the single writer: it applies that
// bookkeeping and the freshly published ceilings, and reconciles which scopes
// survive. A cycle that fails before commit() (e.g. an isolation safety-check
// rollback) leaves no state behind, so a failed cycle can never half-advance
// the ceiling or corrupt the ACK baseline.
package cpu

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/cputypes"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// reclaimScopePhase names the per-scope lifecycle. The transitions are:
//
//	dormant --(scope first appears with a retained ceiling)--> dormant (kept)
//	active, no ACK yet        -> hold    (ceiling held, waiting for first ACK)
//	hold --precise/tolerated ACK--> acked (ceiling steps toward desired)
//	acked/hold --ramp-up exits--> draining (desired=steadyCap, ceiling steps up)
//	draining --ceiling reaches steadyCap + ACK--> retired (state dropped)
//
// hold is a real, reachable phase: an active scope that has not yet had its
// first publication ACK sits in hold (ceiling held, acked gauge 0) rather
// than being optimistically labeled acked. dormant is the paused state kept
// across brief ramp-up flaps so a re-activation reuses the retained legal
// ceiling instead of re-seeding from zero.
type reclaimScopePhase int

const (
	reclaimPhaseDormant reclaimScopePhase = iota
	reclaimPhaseHold
	reclaimPhaseAcked
	reclaimPhaseDraining
)

// quasiAckGraceCycles is the number of consecutive cycles an observed value may
// sit within cpusPerCore of the last published value before it is accepted as a
// tolerated (quasi) ACK. This is a tolerance window for QRM lag, not a
// re-seed: an exact equality remains the primary ACK, and a larger deviation
// resets the grace counter instead of accumulating it.
const quasiAckGraceCycles = 3

// coreAlignedStep rounds the raw ramp step up to a whole-core multiple and floors
// it at one core, so the per-cycle ceiling movement is always an integral number
// of CPUs aligned to the physical core boundary (design: step = max(coreAligned(
// MaxRampUpStep), cpusPerCore)).
func coreAlignedStep(rawStep, cpusPerCore int) int {
	if cpusPerCore <= 0 {
		cpusPerCore = 1
	}
	step := rawStep
	if step < cpusPerCore {
		step = cpusPerCore
	}
	if rem := step % cpusPerCore; rem != 0 {
		step += cpusPerCore - rem
	}
	return step
}

type reclaimConstraintScopeState struct {
	Phase reclaimScopePhase
	// LastPublished is the reclaim pool size published for this scope in the
	// previous cycle; it is compared against the observed size to confirm QRM
	// applied the ceiling (ACK).
	LastPublished int
	// HasPublished is false until the first successful commit that actually
	// published a pool size for this scope. It is never set optimistically: an
	// active scope with no pool publication this cycle keeps HasPublished=false
	// so the first real publication establishes the ACK baseline (a missing pool
	// is not treated as an ACK).
	HasPublished bool
	// HasDescriptor records whether a ramp-up domain descriptor has ever
	// materialized for this scope. It distinguishes "the descriptor was never
	// resolved" (fail closed: stay unconstrained) from "the descriptor resolved
	// to a value of 0" (a legitimate, immediately-retired scope).
	HasDescriptor bool
	// Ceiling is the optional, rate-limited pool upper bound. nil means the scope
	// is unconstrained: the pool keeps its steady size rather than being floored.
	Ceiling *int
	// Desired is the ramp-up target the ceiling converges toward while active.
	Desired int
	// Floor is the pure steady reservation lower bound.
	Floor int
	// SteadyCap is the pool upper bound the scope returns to after ramp-up exits.
	SteadyCap int
	// UnackedCycles counts consecutive within-tolerance (quasi) observations
	// pending a tolerated ACK; exact ACKs and large deviations reset it.
	UnackedCycles int
	// StallCycles is a monotonically increasing count of cycles the scope stayed
	// ACK-stalled (active but the ceiling could not advance because QRM had not
	// acknowledged the published value). Surfaced as a counter so canary can answer
	// "is the ACK stuck, and for how many cycles".
	StallCycles int64
	// MemberNUMAs is the sorted member NUMA set the scope aggregates over.
	MemberNUMAs []int
}

// decidedScope is the pure, per-scope result of one decision cycle. The guard
// computes it in constraint() and commit() is the only place it is applied back
// to g.scopeState, so a cycle that aborts before commit leaves no trace.
type decidedScope struct {
	// ceiling is this cycle's optional upper bound (nil = unconstrained).
	ceiling *int
	// acked reports whether QRM acknowledged the last published value this cycle.
	acked bool
	// unackedCycles is the proposed next quasi-grace counter.
	unackedCycles int
	// stallDelta is 1 when the scope stayed ACK-stalled this cycle, else 0.
	stallDelta int64
}

type reclaimConstraintGuard struct {
	scopeState map[cputypes.ReclaimConstraintScope]reclaimConstraintScopeState
	targets    map[cputypes.ReclaimConstraintScope]types.ReclaimConstraintTarget
}

type reclaimConstraintTarget = types.ReclaimConstraintTarget

// hasRetainedState reports whether the guard holds any per-scope state. It gates
// running the constraint machinery when ramp-up is off but a scope is still
// draining back to steadyCap.
func (g *reclaimConstraintGuard) hasRetainedState() bool {
	return len(g.scopeState) > 0
}

// constraint decides the reclaim constraint and per-scope ceilings for the
// current cycle. activeScopes are the scopes with a live ramp-up domain; the
// guard additionally constrains scopes that are still draining their way back
// to steadyCap after ramp-up exited.
//
// It is pure: the returned ceilings are optional (*int) and the returned
// accounting carries the per-scope ACK bookkeeping that commit() will persist.
// A nil ceiling means the scope is unconstrained this cycle -- used when a scope
// activates but no descriptor / pool observation exists yet, so the pool keeps
// its steady size rather than being yanked to an arbitrary floor.
func (g *reclaimConstraintGuard) constraint(
	activeScopes map[cputypes.ReclaimConstraintScope]bool,
	observedByScope map[cputypes.ReclaimConstraintScope]int,
	observedOKByScope map[cputypes.ReclaimConstraintScope]bool,
	maxRampUpStep int,
	cpusPerCore int,
) (
	cputypes.ReclaimConstraint,
	map[cputypes.ReclaimConstraintScope]*int,
	map[cputypes.ReclaimConstraintScope]bool,
	map[cputypes.ReclaimConstraintScope]decidedScope,
) {
	constrained := g.constrainedScopes(activeScopes)
	if len(constrained) == 0 {
		return cputypes.ReclaimConstraintNone, nil, nil, nil
	}
	if maxRampUpStep <= 0 {
		// No per-scope rate is configured: publish no dynamic ceiling and leave
		// every scope unconstrained at its steady size (the pool keeps steady, it
		// is NOT clamped to a floor). commit() preserves any retained ceiling
		// rather than wiping it, so a transient misconfiguration cannot destroy
		// the anti-flap bookkeeping.
		general.Warningf("[qosaware-cpu] reclaim rate-limit step non-positive (%d): scopes stay unconstrained at steady size", maxRampUpStep)
		ceilings := make(map[cputypes.ReclaimConstraintScope]*int, len(constrained))
		for scope := range constrained {
			ceilings[scope] = nil
		}
		return cputypes.ReclaimConstraintReservedFloor, ceilings, constrained, nil
	}

	// Core-align the rate so every per-cycle ceiling movement is a whole-core
	// multiple (design: step = max(coreAligned(MaxRampUpStep), cpusPerCore)).
	step := coreAlignedStep(maxRampUpStep, cpusPerCore)
	ceilings := make(map[cputypes.ReclaimConstraintScope]*int, len(constrained))
	accounting := make(map[cputypes.ReclaimConstraintScope]decidedScope, len(constrained))
	for scope := range constrained {
		decided := g.decideCeiling(scope, activeScopes[scope], observedByScope, observedOKByScope, step, cpusPerCore)
		ceilings[scope] = decided.ceiling
		accounting[scope] = decided
	}
	return cputypes.ReclaimConstraintReservedFloor, ceilings, constrained, accounting
}

// constrainedScopes returns the union of actively ramped scopes and scopes the
// guard is still draining back to steadyCap.
func (g *reclaimConstraintGuard) constrainedScopes(
	activeScopes map[cputypes.ReclaimConstraintScope]bool,
) map[cputypes.ReclaimConstraintScope]bool {
	constrained := make(map[cputypes.ReclaimConstraintScope]bool, len(activeScopes)+len(g.scopeState))
	for scope := range activeScopes {
		constrained[scope] = true
	}
	for scope, state := range g.scopeState {
		if activeScopes[scope] {
			continue
		}
		// A retained (dormant/draining) scope stays constrained until its ceiling
		// has converged back to steadyCap; an already-retired scope is dropped.
		if scopeRetired(state) {
			continue
		}
		constrained[scope] = true
	}
	return constrained
}

// decideCeiling computes the optional ceiling and the pure ACK bookkeeping for
// one constrained scope. It never mutates g.scopeState; the returned
// decidedScope is applied by commit().
//
// Failure semantics:
//   - an active scope whose ramp-up domain descriptor has never materialized
//     (e.g. resolveGlobalRampUpTarget keeps failing) returns a nil ceiling and
//     no accounting -- the pool keeps its steady size and must NOT ratchet down
//     toward a zero-value desired;
//   - a scope with no pool observation evidence yet stays unconstrained rather
//     than treating the unknown as 0.
func (g *reclaimConstraintGuard) decideCeiling(
	scope cputypes.ReclaimConstraintScope,
	isActive bool,
	observedByScope map[cputypes.ReclaimConstraintScope]int,
	observedOKByScope map[cputypes.ReclaimConstraintScope]bool,
	step int,
	cpusPerCore int,
) decidedScope {
	state, hasState := g.scopeState[scope]
	target, hasTarget := g.targets[scope]

	// Resolve this cycle's desired / floor. A draining scope (no live ramp-up
	// domain) reverts its desired to steadyCap.
	floor := state.Floor
	desired := state.Desired
	if isActive && hasTarget {
		floor = target.Floor
		desired = target.Desired
	} else if !isActive && hasState && state.SteadyCap > 0 {
		desired = state.SteadyCap
	}

	// Fail closed: an active scope that has never received a descriptor must not
	// be constrained against zero-value floor/desired. Stay unconstrained (nil
	// ceiling) until a descriptor actually arrives; a retained descriptor from a
	// previous cycle (HasDescriptor) keeps the anti-flap ceiling valid.
	if isActive && !hasTarget && !state.HasDescriptor {
		general.InfoSV(4, "[qosaware-cpu] scope active but no descriptor yet: staying unconstrained (fail-closed)",
			"scope", string(scope))
		return decidedScope{}
	}

	observed, observedOK := observedByScope[scope], observedOKByScope[scope]

	// Resolve the current ceiling value.
	var current *int
	switch {
	case hasState && state.Ceiling != nil:
		c := *state.Ceiling
		current = &c
	case !hasState && isActive && observedOK:
		// First activation: seed from the pool QRM is actually running, never from
		// zero. The ceiling is held here until the ACK below confirms it.
		c := observed
		if c < floor {
			c = floor
		}
		current = &c
	case !hasState && isActive && !observedOK:
		// No observation evidence: do not treat the unknown as zero. Stay
		// unconstrained and let the pool run steady; the next observed cycle seeds.
		current = nil
	case hasState && state.Ceiling == nil && observedOK:
		c := observed
		if c < floor {
			c = floor
		}
		current = &c
	default:
		current = nil
	}

	if current == nil {
		return decidedScope{}
	}

	// ACK: exact equality is primary; within cpusPerCore for a few consecutive
	// cycles is a tolerated (quasi) ACK; a larger deviation resets the grace
	// counter. Computed against the retained state; applied in commit().
	acked := false
	unacked := state.UnackedCycles
	if hasState && state.HasPublished && observedOK {
		diff := observed - state.LastPublished
		if diff < 0 {
			diff = -diff
		}
		switch {
		case diff == 0:
			acked = true
			unacked = 0
		case cpusPerCore > 0 && diff <= cpusPerCore:
			unacked++
			if unacked >= quasiAckGraceCycles {
				acked = true
				unacked = 0
			}
		default:
			unacked = 0
		}
	}

	ceiling := *current
	stallDelta := int64(0)
	if acked {
		// Bidirectional rate-limit: at most `step` away from the current ceiling,
		// in either direction, toward desired.
		if desired > ceiling {
			ceiling += step
			if ceiling > desired {
				ceiling = desired
			}
		} else if desired < ceiling {
			ceiling -= step
			if ceiling < desired {
				ceiling = desired
			}
		}
	} else if isActive && desired != ceiling {
		// The ceiling is frozen because QRM has not acknowledged the published
		// value yet. Count the stall so operators can see the ACK is stuck.
		stallDelta = 1
		general.InfoSV(4, "[qosaware-cpu] ACK stall: ceiling frozen, waiting for QRM to acknowledge",
			"scope", string(scope), "ceiling", ceiling, "desired", desired,
			"lastPublished", state.LastPublished, "observed", observed, "diff", observed-state.LastPublished)
	}
	// If not acked, hold the current ceiling frozen.

	c := ceiling
	return decidedScope{ceiling: &c, acked: acked, unackedCycles: unacked, stallDelta: stallDelta}
}

// commit records the published per-scope ceilings and ACK baselines after a
// successful update cycle. Unlike a full reset, scopes that leave the active
// set are retained (dormant/draining) rather than dropped, so a brief ramp-up
// flap does not re-seed the ceiling from zero. A scope is dropped only once it
// has drained back to steadyCap and published there, or when its members
// disappear entirely from the current scope numbering.
func (g *reclaimConstraintGuard) commit(
	activeScopes map[cputypes.ReclaimConstraintScope]bool,
	ceilings map[cputypes.ReclaimConstraintScope]*int,
	accounting map[cputypes.ReclaimConstraintScope]decidedScope,
	targets map[string]reclaimConstraintTarget,
	publishedByScope map[cputypes.ReclaimConstraintScope]int,
	scopeNumas map[cputypes.ReclaimConstraintScope][]int,
) {
	// Only overwrite the descriptor cache when the assembler actually recorded one
	// this cycle. An empty map means "no descriptors published" (e.g. a draining
	// cycle whose domain descriptor is gone); keep the retained descriptors so the
	// ACK bookkeeping and steadyCap lookup stay valid. The descriptor cache lags a
	// cycle by design: it reflects what the assembler last published, not the
	// in-flight domain set.
	if len(targets) > 0 {
		g.targets = make(map[cputypes.ReclaimConstraintScope]types.ReclaimConstraintTarget, len(targets))
		for scopeStr, target := range targets {
			g.targets[cputypes.ReclaimConstraintScope(scopeStr)] = target
		}
	} else if g.targets == nil {
		g.targets = make(map[cputypes.ReclaimConstraintScope]types.ReclaimConstraintTarget)
	}

	if g.scopeState == nil {
		g.scopeState = make(map[cputypes.ReclaimConstraintScope]reclaimConstraintScopeState)
	}

	// constrainedScopes is recomputed here (it is pure over the pre-decision
	// state) so commit iterates the same set constraint() decided on.
	constrained := g.constrainedScopes(activeScopes)
	newState := make(map[cputypes.ReclaimConstraintScope]reclaimConstraintScopeState, len(g.scopeState)+len(activeScopes))

	for scope := range constrained {
		state := g.scopeState[scope]

		// Persist the freshly decided ceiling. A non-nil ceiling is the bound the
		// guard published this cycle. A nil ceiling means the scope is unconstrained
		// (e.g. the per-cycle rate configured non-positive, or no observation yet):
		// the pool ran its natural, unconstrained size rather than a rate-limited
		// bound. Anchor the retained ceiling to that published size so
		// re-constraining resumes from where the pool actually is -- instead of
		// clamping it back down by more than one rate-limit step on recovery. The
		// scope is still retained (never wiped), so the anti-flap bookkeeping holds.
		if ceiling, ok := ceilings[scope]; ok && ceiling != nil {
			state.Ceiling = ceiling
		} else if pub, ok := publishedByScope[scope]; ok && state.HasDescriptor {
			c := pub
			state.Ceiling = &c
		}

		if target, ok := g.targets[scope]; ok {
			state.Floor = target.Floor
			state.SteadyCap = target.SteadyCap
			state.MemberNUMAs = target.MemberNUMAs
			state.Desired = target.Desired
			// A descriptor arrived this cycle: the scope is no longer in the
			// "never had a descriptor" fail-closed state.
			state.HasDescriptor = true
		}
		// A scope that has left the active set drains its desired back to steadyCap.
		if !activeScopes[scope] && state.SteadyCap > 0 {
			state.Desired = state.SteadyCap
		}

		// Apply the pure ACK bookkeeping decided this cycle.
		if acct, ok := accounting[scope]; ok {
			state.UnackedCycles = acct.unackedCycles
			state.StallCycles += acct.stallDelta
		}

		if pub, ok := publishedByScope[scope]; ok {
			state.LastPublished = pub
			state.HasPublished = true
		}
		// ACK-baseline invariant: an active scope with no pool publication this cycle keeps
		// HasPublished=false; the first real publication establishes the ACK
		// baseline rather than faking one (which would manufacture a false ACK).

		// Phase is assigned from the real ACK state this cycle: an active scope
		// that has not acked sits in hold, not optimistically in acked.
		switch {
		case activeScopes[scope]:
			if acct, ok := accounting[scope]; ok && acct.acked {
				state.Phase = reclaimPhaseAcked
			} else {
				state.Phase = reclaimPhaseHold
			}
		default:
			state.Phase = reclaimPhaseDraining
		}
		newState[scope] = state
	}

	// Drop state once a draining scope has converged back to steadyCap. A scope
	// whose member NUMA set vanished from the current scope numbering is dropped
	// ONLY when it is already retired: a scope still draining (ceiling below
	// steadyCap) must be retained even if it is absent from this cycle's published
	// scope set, otherwise the next cycle re-seeds from zero and the ceiling jumps
	// straight back up to steadyCap.
	for scope, state := range newState {
		if !activeScopes[scope] && scopeRetired(state) {
			delete(newState, scope)
			continue
		}
		// A scope that left this cycle's published scope numbering (its member NUMA
		// set vanished from the region map) is dropped ONLY once it has retired. The
		// previous `!scopeRetired` here was inverted: it deleted the very scopes that
		// are still draining (ceiling below steadyCap) the moment their region
		// flapped out for a single cycle, forcing the next cycle to re-seed from zero
		// and yank the ceiling straight back up to steadyCap. A non-retired scope has a
		// convergence state worth preserving, so it survives even when absent.
		if _, ok := scopeNumas[scope]; !ok && scopeRetired(state) {
			delete(newState, scope)
		}
	}

	g.scopeState = newState
}

// scopeRetired reports whether a retained scope has nothing left to do and can
// be dropped: it either never received a descriptor (fail-closed, was never
// meaningfully constrained), publishes no ceiling (unconstrained), or has
// climbed its ceiling back up to steadyCap. This distinguishes "the descriptor
// was never resolved" from "the descriptor resolved to 0" -- the latter still
// carries HasDescriptor and converges immediately because ceiling >= 0 always.
func scopeRetired(state reclaimConstraintScopeState) bool {
	if !state.HasDescriptor {
		return true
	}
	if state.Ceiling == nil {
		return true
	}
	return *state.Ceiling >= state.SteadyCap
}

// reclaimScopeDiagnostics is the per-cycle, read-only view of a scope's guard
// state, surfaced as metrics so operators can watch the rate-limited ceiling
// converge toward the desired target without mistaking it for the pool size the
// assembler actually publishes.
type reclaimScopeDiagnostics struct {
	Scope         string
	Phase         string
	Ceiling       int64 // -1 when the ceiling is nil (unconstrained)
	HasCeiling    bool
	Desired       int64
	Floor         int64
	SteadyCap     int64
	LastPublished int64
	UnackedCycles int64
	// Acked is 1 when the scope is in the acked phase (it published and really
	// ACKed the value last cycle); a hold-phase scope reports 0.
	Acked int64
	// StallCycles is the cumulative count of ACK-stalled cycles (counter).
	StallCycles int64
}

func phaseName(p reclaimScopePhase) string {
	switch p {
	case reclaimPhaseDormant:
		return "dormant"
	case reclaimPhaseHold:
		return "hold"
	case reclaimPhaseAcked:
		return "acked"
	case reclaimPhaseDraining:
		return "draining"
	default:
		return "unknown"
	}
}

// snapshot returns the current per-scope guard state for metrics emission.
func (g *reclaimConstraintGuard) snapshot() []reclaimScopeDiagnostics {
	out := make([]reclaimScopeDiagnostics, 0, len(g.scopeState))
	for scope, state := range g.scopeState {
		d := reclaimScopeDiagnostics{
			Scope:         string(scope),
			Phase:         phaseName(state.Phase),
			Desired:       int64(state.Desired),
			Floor:         int64(state.Floor),
			SteadyCap:     int64(state.SteadyCap),
			LastPublished: int64(state.LastPublished),
			UnackedCycles: int64(state.UnackedCycles),
			StallCycles:   state.StallCycles,
		}
		if state.Phase == reclaimPhaseAcked {
			d.Acked = 1
		}
		if state.Ceiling != nil {
			d.HasCeiling = true
			d.Ceiling = int64(*state.Ceiling)
		} else {
			d.Ceiling = -1
		}
		out = append(out, d)
	}
	return out
}
