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
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/deadlock"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ErrNoProgress reports that a fixed-point compilation could not advance toward
// ParentSafe: a projected phase produced no state change, so freezing the trace
// would loop forever instead of converging.
var ErrNoProgress = errors.New("fixed-point compilation made no progress")

type ProtectedTransferStallError struct {
	CPUs machine.CPUSet
}

func (e *ProtectedTransferStallError) Error() string {
	return fmt.Sprintf("%v: all pending transfer CPUs remain protected: cpus=%s",
		ErrNoProgress, e.CPUs.String())
}

func (e *ProtectedTransferStallError) Unwrap() error { return ErrNoProgress }

type phaseProgressKey struct {
	SnapshotID SnapshotID
	PlanID     string
}

type ProjectedPhaseCycleError struct {
	SnapshotID SnapshotID
	PlanID     string
}

func (e *ProjectedPhaseCycleError) Error() string {
	return fmt.Sprintf("%v: projected phase cycle snapshot=%x plan=%s",
		ErrNoProgress, e.SnapshotID, e.PlanID)
}

func (e *ProjectedPhaseCycleError) Unwrap() error { return ErrNoProgress }

type ProjectedPhaseNoProgressError struct {
	SnapshotID SnapshotID
	PlanID     string
	Operations int
}

func (e *ProjectedPhaseNoProgressError) Error() string {
	return fmt.Sprintf("%v: projected frontier had no effect snapshot=%x plan=%s operations=%d",
		ErrNoProgress, e.SnapshotID, e.PlanID, e.Operations)
}

func (e *ProjectedPhaseNoProgressError) Unwrap() error { return ErrNoProgress }

type convergenceDeadlineContextError struct {
	cause               error
	hasCoordinatorState bool
	rounds              int
	usage               BudgetUsage
}

func (e *convergenceDeadlineContextError) Error() string {
	if !e.hasCoordinatorState {
		return fmt.Sprintf("%v: %v", ErrConvergenceDeadlineExceeded, e.cause)
	}
	return fmt.Sprintf("%v: %v after rounds=%d usage=%+v",
		ErrConvergenceDeadlineExceeded, e.cause, e.rounds, e.usage)
}

func (e *convergenceDeadlineContextError) Unwrap() error { return e.cause }

func (e *convergenceDeadlineContextError) Is(target error) bool {
	return target == ErrConvergenceDeadlineExceeded || errors.Is(e.cause, target)
}

// classifyContextExit maps context cancellation and deadline errors onto the
// convergence-deadline taxonomy while preserving the original context cause
// for errors.Is. It attaches projected round and budget usage at most once and
// leaves non-context errors unchanged.
func (r *coordinatorRound) classifyContextExit(err error) error {
	if err == nil || (!errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded)) {
		return err
	}
	var classified *convergenceDeadlineContextError
	if errors.As(err, &classified) && classified.hasCoordinatorState {
		return err
	}
	cause := err
	if classified != nil {
		cause = classified.cause
	}
	classified = &convergenceDeadlineContextError{cause: cause}
	if r != nil && r.budget != nil {
		classified.hasCoordinatorState = true
		classified.rounds = r.round
		classified.usage = r.budget.Usage()
	}
	return classified
}

// CompiledPhase is one ordered, frozen phase of a fixed-point trace. Operations
// are already sequenced; execution replays them verbatim without re-planning.
type CompiledPhase struct {
	Kind       PhaseKind
	Operations []PlanOperation
}

// CompiledPhaseTrace is the frozen Drain->Expand fixed point compiled on a
// cloned snapshot. Phases are strictly ordered and FinalEvaluation records the
// convergence and parent-safety proof observed on the projected end state.
type CompiledPhaseTrace struct {
	TraceID              string
	ConvergenceID        string
	Objective            ConvergenceObjective
	InitialSnapshot      *CompleteSnapshot
	CanonicalTargetByRel map[string]CPUSetTarget
	RequiredCPUSetByRel  map[string]machine.CPUSet
	Capabilities         HierarchyCapabilities
	EvaluationInput      FrozenCoordinatorEvaluationInput
	FrozenBoundary       FrozenBoundary
	RollbackRetirements  []FrozenRollbackRetirement
	Phases               []CompiledPhase
	FinalSnapshot        *CompleteSnapshot
	FinalEvaluation      coordinatorSnapshotEvaluation
	Cost                 ExecutionReservationCost
}

// FrozenRollbackRetirement is compiler-owned authority to treat one exact
// generation of a physically written dynamic, non-controlled cgroup as retired
// while replaying rollback. The target and identity must be proven by the
// FrozenBoundary relevant-holder path; controlled roots and trust anchors are
// never eligible.
type FrozenRollbackRetirement struct {
	Rel      string
	Identity CgroupIdentity
}

// validatedPhaseTrace is the compiler-owned immutable carrier used by the
// production admission chain. Its trace must never escape to mutable callers;
// every internal consumer relies on the single successful freeze recorded here.
type validatedPhaseTrace struct {
	frozen *CompiledPhaseTrace
}

func (t *validatedPhaseTrace) trace() (*CompiledPhaseTrace, error) {
	if t == nil || t.frozen == nil {
		return nil, fmt.Errorf("validated phase trace is unavailable")
	}
	return t.frozen, nil
}

func (t *CompiledPhaseTrace) OperationCount() int {
	if t == nil {
		return 0
	}
	count := 0
	for _, phase := range t.Phases {
		count += len(phase.Operations)
	}
	return count
}

// FrozenCoordinatorEvaluationInput owns every semantic input consumed by the
// production coordinator evaluator. FreezePhaseTrace rebuilds the DAG and
// recomputes FinalEvaluation from this immutable evidence instead of maintaining
// a second, reduced evaluator.
type FrozenCoordinatorEvaluationInput struct {
	DAGSpecs                []NodeSpec
	TargetByRel             map[string]machine.CPUSet
	ParentSafetyTargetByRel map[string]machine.CPUSet
	TargetMemsByRel         map[string]string
	DesiredByDomain         map[DomainID]machine.CPUSet
	AllowedCPUs             machine.CPUSet
	ExpectedByRel           map[string]machine.CPUSet
	RequiredByRel           map[string]machine.CPUSet
	DeferredByRel           map[string]machine.CPUSet
	DeferredCleanupRels     map[string]struct{}
	ProtectedPending        machine.CPUSet
	PendingRequiredByRel    map[string]machine.CPUSet
	Capabilities            HierarchyCapabilities
	AllowEmptyTarget        bool
}

type fixedPointEngineMode uint8

const (
	fixedPointEngineUntilObjective fixedPointEngineMode = iota
	fixedPointEngineSingleRound
)

// phaseSessionApplyResult is execution-neutral evidence returned by a session.
// A live session returns the safe-writer journal prefix; a projected session
// returns the equivalent projected operations.
type phaseSessionApplyResult struct {
	Journal []AppliedPlanOperation
	Applied int
}

type fixedPointEngineResult struct {
	ConvergenceID        string
	CanonicalTargetByRel map[string]CPUSetTarget
	Phases               []CompiledPhase
	InitialSnapshot      *CompleteSnapshot
	FinalSnapshot        *CompleteSnapshot
	FinalEvaluation      coordinatorSnapshotEvaluation
	Outcome              RoundOutcome
	Rounds               int
	ObjectiveSatisfied   bool
}

func (in FrozenCoordinatorEvaluationInput) evaluate(
	snapshot *CompleteSnapshot,
) (coordinatorSnapshotEvaluation, error) {
	dag, err := BuildDAG(cloneNodeSpecs(in.DAGSpecs))
	if err != nil {
		return coordinatorSnapshotEvaluation{}, fmt.Errorf("rebuild frozen evaluation DAG: %w", err)
	}
	return evaluateCoordinatorSnapshot(
		snapshot, dag, in.TargetByRel, in.ParentSafetyTargetByRel, in.TargetMemsByRel,
		in.DesiredByDomain, in.AllowedCPUs, in.ExpectedByRel, in.RequiredByRel,
		in.DeferredByRel, in.DeferredCleanupRels, in.ProtectedPending,
		in.PendingRequiredByRel,
		in.Capabilities, in.AllowEmptyTarget,
	)
}

// phaseExecutionSession abstracts the surface the fixed-point engine drives:
// it reads a complete snapshot and applies an ordered phase. The compiler binds
// it to a clone-backed projection; live replay binds it to the real driver.
type phaseExecutionSession interface {
	Snapshot(ctx context.Context) (*CompleteSnapshot, error)
	Apply(ctx context.Context, plan PhasePlan) (phaseSessionApplyResult, error)
	Capabilities() HierarchyCapabilities
}

type projectedPhaseSession struct {
	hierarchy *projectedHierarchy
	progress  map[phaseProgressKey]struct{}
}

type livePhaseSession struct {
	round *coordinatorRound
	res   *ConvergenceResult
}

func newLivePhaseSession(round *coordinatorRound, res *ConvergenceResult) *livePhaseSession {
	return &livePhaseSession{round: round, res: res}
}

func (s *livePhaseSession) Snapshot(ctx context.Context) (*CompleteSnapshot, error) {
	return s.round.nextSnapshot(ctx)
}

func (s *livePhaseSession) Apply(ctx context.Context, plan PhasePlan) (phaseSessionApplyResult, error) {
	journalStart := 0
	appliedStart := 0
	if s.res != nil {
		journalStart = len(s.res.Journal)
		appliedStart = s.res.Applied
	}
	s.round.planID = plan.PlanID
	err := s.round.executePlan(ctx, plan, s.res)
	result := phaseSessionApplyResult{}
	if s.res != nil {
		result.Journal = append(result.Journal, s.res.Journal[journalStart:]...)
		result.Applied = s.res.Applied - appliedStart
	}
	return result, err
}

func (s *livePhaseSession) Capabilities() HierarchyCapabilities {
	return s.round.driver.Capabilities()
}

func newProjectedPhaseSession(
	base *CompleteSnapshot,
	capabilities HierarchyCapabilities,
) (*projectedPhaseSession, error) {
	hierarchy, err := newProjectedHierarchy(base, capabilities)
	if err != nil {
		return nil, err
	}
	return &projectedPhaseSession{
		hierarchy: hierarchy,
		progress:  make(map[phaseProgressKey]struct{}),
	}, nil
}

func (s *projectedPhaseSession) Snapshot(ctx context.Context) (*CompleteSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return nil, &convergenceDeadlineContextError{cause: err}
	}
	snapshot := CloneCompleteSnapshot(s.hierarchy.snapshot)
	if err := ctx.Err(); err != nil {
		return nil, &convergenceDeadlineContextError{cause: err}
	}
	return snapshot, nil
}

// Apply evaluates a frontier against an isolated candidate and commits both
// the projected snapshot and its progress key only after every operation and
// evidence settlement succeeds. Cancellation cannot consume progress or make
// a retry appear to be a projected cycle.
func (s *projectedPhaseSession) Apply(ctx context.Context, plan PhasePlan) (phaseSessionApplyResult, error) {
	if err := ctx.Err(); err != nil {
		return phaseSessionApplyResult{}, &convergenceDeadlineContextError{cause: err}
	}
	if len(plan.Operations) == 0 {
		return phaseSessionApplyResult{}, nil
	}
	key := phaseProgressKey{SnapshotID: s.hierarchy.snapshot.ID, PlanID: plan.PlanID}
	if _, repeated := s.progress[key]; repeated {
		return phaseSessionApplyResult{}, &ProjectedPhaseCycleError{
			SnapshotID: key.SnapshotID,
			PlanID:     key.PlanID,
		}
	}
	candidate, err := newProjectedHierarchy(s.hierarchy.snapshot, s.hierarchy.capabilities)
	if err != nil {
		return phaseSessionApplyResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return phaseSessionApplyResult{}, &convergenceDeadlineContextError{cause: err}
	}
	candidate.evidenceRebuilds = s.hierarchy.evidenceRebuilds
	if err := validateProjectedFrontierIndependence(candidate, plan.Operations); err != nil {
		return phaseSessionApplyResult{}, err
	}
	result := phaseSessionApplyResult{}
	for _, operation := range plan.Operations {
		if err := ctx.Err(); err != nil {
			return result, &convergenceDeadlineContextError{cause: err}
		}
		if err := candidate.applyConfiguredOperation(operation); err != nil {
			return result, err
		}
		result.Applied++
		result.Journal = append(result.Journal, AppliedPlanOperation{
			PlanID: operation.PlanID, Rel: operation.Rel, Direction: operation.Direction,
			Target: operation.Target, Observed: operation.Target,
		})
	}
	if result.Applied != len(plan.Operations) {
		return result, fmt.Errorf("projected frontier applied=%d operations=%d",
			result.Applied, len(plan.Operations))
	}
	if err := ctx.Err(); err != nil {
		return result, &convergenceDeadlineContextError{cause: err}
	}
	if err := candidate.settleEvidence(); err != nil {
		return result, err
	}
	if err := ctx.Err(); err != nil {
		return result, &convergenceDeadlineContextError{cause: err}
	}
	if candidate.snapshot.ID == key.SnapshotID {
		return phaseSessionApplyResult{}, &ProjectedPhaseNoProgressError{
			SnapshotID: key.SnapshotID,
			PlanID:     key.PlanID,
			Operations: len(plan.Operations),
		}
	}
	*s.hierarchy = *candidate
	s.progress[key] = struct{}{}
	return result, nil
}

func (s *projectedPhaseSession) Capabilities() HierarchyCapabilities {
	return s.hierarchy.capabilities
}

// compileFixedPointTrace compiles a complete, frozen Drain->Expand trace on a
// clone of base without touching the live hierarchy driver or mutable round
// state.
func (r *coordinatorRound) compileFixedPointTrace(
	ctx context.Context,
	base *CompleteSnapshot,
) (*CompiledPhaseTrace, error) {
	validated, err := r.compileValidatedFixedPointTrace(ctx, base)
	if err != nil {
		return nil, err
	}
	return validated.trace()
}

// compileValidatedFixedPointTrace is the sole owner of compilation and the
// validation freeze for the production ParentSafe chain. It closes the trace
// over initial and final snapshot evidence, evaluation input, frozen boundary,
// phases, and reservation cost before publishing the carrier. Failure publishes
// no carrier; success transfers immutable read-only ownership to reservation,
// preflight, and execution, which never expose or mutate the trace.
func (r *coordinatorRound) compileValidatedFixedPointTrace(
	ctx context.Context,
	base *CompleteSnapshot,
) (*validatedPhaseTrace, error) {
	if r == nil {
		return nil, fmt.Errorf("compile fixed-point trace requires a coordinator round")
	}
	if base == nil {
		return nil, fmt.Errorf("compile fixed-point trace requires a base snapshot")
	}
	if r.budget == nil {
		return nil, fmt.Errorf("compile fixed-point trace requires a convergence budget")
	}
	capabilities := base.Capabilities
	if err := ctx.Err(); err != nil {
		return nil, r.classifyContextExit(err)
	}
	session, err := newProjectedPhaseSession(base, capabilities)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, r.classifyContextExit(err)
	}
	projectedRound := r.cloneForProjection()
	result, err := projectedRound.runFixedPointEngine(ctx, session, fixedPointEngineUntilObjective)
	if err != nil {
		return nil, projectedRound.classifyContextExit(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, projectedRound.classifyContextExit(err)
	}
	evaluationInput := freezeCoordinatorEvaluationInput(projectedRound, capabilities)
	frozenBoundary, err := compileFrozenBoundaryV1(base, evaluationInput, result.Phases)
	if err != nil {
		return nil, fmt.Errorf("compile frozen boundary: %w", err)
	}
	rollbackRetirements, err := compileFrozenRollbackRetirements(
		base, frozenBoundary, result.Phases)
	if err != nil {
		return nil, fmt.Errorf("compile rollback retirements: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return nil, projectedRound.classifyContextExit(err)
	}
	trace := &CompiledPhaseTrace{
		ConvergenceID:        result.ConvergenceID,
		Objective:            projectedRound.objective.orFullDefault(),
		InitialSnapshot:      CloneCompleteSnapshot(base),
		CanonicalTargetByRel: cloneCPUSetTargetMap(result.CanonicalTargetByRel),
		RequiredCPUSetByRel:  model.CloneCPUSetMap(projectedRound.requiredByRel),
		Capabilities:         capabilities,
		EvaluationInput:      evaluationInput,
		FrozenBoundary:       frozenBoundary,
		RollbackRetirements:  rollbackRetirements,
		Phases:               cloneCompiledPhases(result.Phases),
		FinalSnapshot:        CloneCompleteSnapshot(result.FinalSnapshot),
		FinalEvaluation:      cloneCoordinatorSnapshotEvaluation(result.FinalEvaluation),
		Cost:                 executionReservationCost(result.Phases),
	}
	validated, err := freezeValidatedPhaseTrace(ctx, trace)
	if err != nil {
		return nil, projectedRound.classifyContextExit(err)
	}
	return validated, nil
}

// checkEngineDeadline fails fast at the top of every fixed-point round.
//
// The projected session used by compileFixedPointTrace performs Snapshot/Apply
// purely in memory, so it never routes through the budgeted hierarchy driver
// that enforces the convergence deadline on each I/O. Without this guard a
// non-converging (thrashing) projection would silently consume the entire
// admission handler timeout inside compile, leaving no budget for
// executeFrozenTrace and surfacing as "admission parent-safe deadline
// exceeded" with attempted=0 applied=0 (the coordinator never attempts a
// physical write).
//
// Both context cancellation/deadline and the budget's absolute Deadline are
// wrapped in ErrConvergenceDeadlineExceeded so callers classify the failure
// as a convergence-budget error (fail-closed), while the underlying context
// error stays on the chain for errors.Is. Used rounds and budget usage are
// annotated for diagnosis.
func (r *coordinatorRound) checkEngineDeadline(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return r.classifyContextExit(err)
	}
	if r.budget != nil && !r.budget.limit.Deadline.IsZero() &&
		!time.Now().Before(r.budget.limit.Deadline) {
		return fmt.Errorf("%w: budget_deadline=%s after rounds=%d usage=%+v",
			ErrConvergenceDeadlineExceeded,
			r.budget.limit.Deadline.Format(time.RFC3339Nano), r.round, r.budget.Usage())
	}
	return nil
}

// runFixedPointEngine drives the shared fixed-point engine against an execution
// session, returning the ordered trace it produced.
//
// The engine is session-agnostic: it only decides and sequences operations,
// reading state through session.Snapshot and mutating it through session.Apply.
// Given the same starting snapshot, a clone-backed projection and a live driver
// therefore compile a byte-for-byte identical Drain->Expand phase sequence. The
// same buildPlan/drainFrontier/rebaseDrainPlan decision helpers that back the
// live coordinator are reused verbatim so compiled and live traces cannot drift.
//
// Each round drains the current frontier, accumulates the release witnesses that
// authorize the following expand, expands toward the desired envelope, and then
// evaluates the projected end state. The loop advances until the snapshot is
// fully converged or, under the ParentSafe objective, proven parent-safe. A
// round that mutates nothing while still short of the objective cannot advance a
// frozen trace, so the engine returns ErrNoProgress instead of looping forever.
func (r *coordinatorRound) runFixedPointEngine(
	ctx context.Context,
	session phaseExecutionSession,
	modes ...fixedPointEngineMode,
) (*fixedPointEngineResult, error) {
	if session == nil {
		return nil, fmt.Errorf("fixed-point engine requires an execution session")
	}
	if r.dag == nil {
		return nil, fmt.Errorf("fixed-point engine requires a bound DAG")
	}
	capabilities := session.Capabilities()
	parentSafetyTargets := parentSafetySemanticTargets(r)
	mode := fixedPointEngineUntilObjective
	if len(modes) > 0 {
		mode = modes[0]
	}

	phases := make([]CompiledPhase, 0)
	var convergenceID string
	var canonicalTargets map[string]CPUSetTarget
	var initialSnapshot *CompleteSnapshot
	errorResult := func(snapshot *CompleteSnapshot, journal []AppliedPlanOperation, blocker error) *fixedPointEngineResult {
		return &fixedPointEngineResult{
			ConvergenceID:        convergenceID,
			CanonicalTargetByRel: cloneCPUSetTargetMap(canonicalTargets),
			Phases:               cloneCompiledPhases(phases),
			InitialSnapshot:      CloneCompleteSnapshot(initialSnapshot),
			FinalSnapshot:        CloneCompleteSnapshot(snapshot),
			Outcome: RoundOutcome{
				Status: RoundStatusStale, Snapshot: CloneCompleteSnapshot(snapshot),
				Blocker: blocker, Journal: append([]AppliedPlanOperation(nil), journal...),
				Cost: r.budget.Usage(),
			},
			Rounds: r.round,
		}
	}
	for {
		if err := r.checkEngineDeadline(ctx); err != nil {
			return nil, err
		}
		if r.round >= r.maxRounds {
			return nil, fmt.Errorf("%w: limit=%d used=%d", ErrRoundBudgetExceeded, r.maxRounds, r.round)
		}
		if err := r.budget.ConsumeRound(); err != nil {
			return nil, err
		}
		r.round++
		r.deferredCleanupRels = make(map[string]struct{})

		start, err := session.Snapshot(ctx)
		if err != nil {
			return errorResult(nil, nil, err), err
		}
		if initialSnapshot == nil {
			initialSnapshot = CloneCompleteSnapshot(start)
		}
		journal := make([]AppliedPlanOperation, 0)

		drain, err := r.buildPlan(ctx, PhaseDrain, start)
		if err != nil {
			var structural *deadlock.StructuralV1NonEmptyDeadlock
			if errors.As(err, &structural) {
				result := errorResult(start, nil, err)
				result.Outcome.Status = RoundStatusBlocked
				return result, nil
			}
			return errorResult(start, nil, err), err
		}
		if convergenceID == "" {
			convergenceID = drain.ConvergenceID
			canonicalTargets = cloneCPUSetTargetMap(drain.CanonicalTargetByRel)
		}
		fresh, released, drainJournal, err := r.applyDrainPhases(ctx, session, drain, &phases)
		journal = append(journal, drainJournal...)
		changedRels := verifiedDrainProgressRels(drain.Base, fresh, drain.TargetByRel)
		if err != nil {
			result := errorResult(fresh, journal, err)
			result.Outcome.ChangedRels = changedRels
			result.Outcome.Progress.DrainChangedRels = len(changedRels)
			return result, err
		}

		r.witnesses = r.witnesses[:0]
		for source, destinations := range released {
			for destination, cpus := range destinations {
				if cpus.IsEmpty() {
					continue
				}
				witness := NewReleaseWitness(drain.ConvergenceID, source, destination, cpus, fresh)
				if witness.CPUs.IsEmpty() {
					continue
				}
				r.witnesses = append(r.witnesses, witness)
			}
		}

		expand, err := r.buildPlan(ctx, PhaseExpand, fresh)
		if err != nil {
			return errorResult(fresh, journal, err), err
		}
		for len(expand.Operations) > 0 {
			frontier, frontierErr := drainFrontier(expand)
			if frontierErr != nil {
				return errorResult(fresh, journal, frontierErr), frontierErr
			}
			applyResult, err := session.Apply(ctx, frontier)
			journal = append(journal, applyResult.Journal...)
			if err != nil {
				recovered, snapshotErr := session.Snapshot(ctx)
				if snapshotErr == nil {
					fresh = recovered
				}
				return errorResult(fresh, journal, err), err
			}
			phases = append(phases, CompiledPhase{
				Kind:       PhaseExpand,
				Operations: append([]PlanOperation(nil), frontier.Operations...),
			})
			expand.Operations = expand.Operations[len(frontier.Operations):]
		}

		final, err := session.Snapshot(ctx)
		if err != nil {
			return errorResult(fresh, journal, err), err
		}
		r.recomputeBlocked(final)

		evaluation, err := evaluateCoordinatorSnapshot(
			final, r.dag, r.targetByRel, parentSafetyTargets, r.desiredMemsByRel(),
			r.desiredDomainUnion(), r.allowedCPUs(),
			r.dynamicByRel, r.requiredByRel, r.deferredByRel,
			r.deferredCleanupRels,
			r.admissionSafetyCPUSet(), r.pendingRequiredByRel,
			capabilities, r.allowEmptyTarget,
		)
		if err != nil {
			return nil, err
		}

		parentSafe := r.objective == ConvergenceObjectiveParentSafe && evaluation.ParentSafety.Safe
		objectiveSatisfied := evaluation.Report.FullyConverged || parentSafe
		status := RoundStatusProgress
		if objectiveSatisfied {
			status = RoundStatusConverged
		} else if len(journal) == 0 && final.ID == start.ID {
			status = RoundStatusBlocked
		}
		outcome := RoundOutcome{
			Status: status, Snapshot: CloneCompleteSnapshot(final),
			Witnesses:   append([]ReleaseWitness(nil), r.witnesses...),
			Journal:     append([]AppliedPlanOperation(nil), journal...),
			ChangedRels: append([]string(nil), changedRels...),
			Progress: ProgressMeasure{
				DrainChangedRels: len(changedRels),
				VerifiedWrites:   len(journal),
			},
			Cost: r.budget.Usage(),
		}
		result := &fixedPointEngineResult{
			ConvergenceID:        convergenceID,
			CanonicalTargetByRel: canonicalTargets,
			Phases:               phases,
			InitialSnapshot:      initialSnapshot,
			FinalSnapshot:        CloneCompleteSnapshot(final),
			FinalEvaluation:      evaluation,
			Outcome:              outcome,
			Rounds:               r.round,
			ObjectiveSatisfied:   objectiveSatisfied,
		}
		if objectiveSatisfied || mode == fixedPointEngineSingleRound {
			return result, nil
		}
		if protected := protectedTransferStallCPUs(drain, r, journal); !protected.IsEmpty() {
			return nil, &ProtectedTransferStallError{CPUs: protected}
		}
		if final.ID == start.ID {
			return nil, &FixedPointStallError{
				ProtectedPending:      r.protectedPending.Clone(),
				ProtectedInCurrentDom: protectedPendingOverlapByDomain(final, r.protectedPending),
				UnmetTargets:          unmetTargetRels(final, r.targetByRel),
				Objective:             r.objective.orFullDefault(),
				Evaluation:            evaluation,
			}
		}
	}
}

// FixedPointStallError is the bare no-progress stall of the fixed-point engine
// enriched with the evidence needed to tell which side blocks: the protected
// pending set, the part of it each current domain still holds, the controlled
// rels whose current cpuset differs from their target, and the final objective
// evaluation. unmet_targets only covers controlled roots and partitions; leaf
// expectations, pending scopes, required floors and domain overlap are reported
// from the evaluation so a stall with unmet_targets=[] still names its blocker.
// It still unwraps to ErrNoProgress so every existing errors.Is classification
// holds.
type FixedPointStallError struct {
	ProtectedPending      machine.CPUSet
	ProtectedInCurrentDom map[DomainID]machine.CPUSet
	UnmetTargets          []string
	Objective             ConvergenceObjective
	Evaluation            coordinatorSnapshotEvaluation
}

const fixedPointStallMaxListedItems = 8

func (e *FixedPointStallError) Error() string {
	domains := make([]string, 0, len(e.ProtectedInCurrentDom))
	for id, cpus := range e.ProtectedInCurrentDom {
		domains = append(domains, fmt.Sprintf("%v=%s", id, cpus.String()))
	}
	sort.Strings(domains)
	unmet := e.UnmetTargets
	suffix := ""
	if len(unmet) > fixedPointStallMaxListedItems {
		suffix = fmt.Sprintf(",+%d", len(unmet)-fixedPointStallMaxListedItems)
		unmet = unmet[:fixedPointStallMaxListedItems]
	}
	return fmt.Sprintf("%v: protected_pending=%s protected_in_current_domain=%v unmet_targets=%v%s%s",
		ErrNoProgress, e.ProtectedPending.String(), domains, unmet, suffix, e.evaluationSummary())
}

// evaluationSummary renders only the non-empty blockers of the final objective
// evaluation, so a healthy field never adds noise to the stall line.
func (e *FixedPointStallError) evaluationSummary() string {
	if e.Objective == "" {
		return ""
	}
	parts := []string{fmt.Sprintf("objective=%s", e.Objective)}
	report := e.Evaluation.Report
	safety := e.Evaluation.ParentSafety
	if len(report.NonConvergedTargets) > 0 {
		parts = append(parts, "non_converged="+formatRelConvergences(report.NonConvergedTargets))
	}
	if e.Objective == ConvergenceObjectiveParentSafe {
		parts = append(parts, fmt.Sprintf("parent_safe=%v", safety.Safe))
		appendCPUSet := func(name string, cpus machine.CPUSet) {
			if !cpus.IsEmpty() {
				parts = append(parts, name+"="+cpus.String())
			}
		}
		appendCPUSet("pending_outside_primary", safety.PendingOutsidePrimary)
		appendCPUSet("pending_inside_reclaim", safety.PendingInsideReclaim)
		appendCPUSet("primary_reclaim_overlap", safety.PrimaryReclaimOverlap)
		if len(safety.PendingScopeDeficit) > 0 {
			parts = append(parts, "pending_scope_deficit="+formatCPUSetMap(safety.PendingScopeDeficit))
		}
		if len(safety.RequiredFloorDeficit) > 0 {
			parts = append(parts, "required_floor_deficit="+formatCPUSetMap(safety.RequiredFloorDeficit))
		}
		if len(safety.UnsafeRequiredRels) > 0 {
			parts = append(parts, "unsafe_required="+formatRelConvergences(safety.UnsafeRequiredRels))
		}
		if len(safety.DeferredLeafMismatches) > 0 {
			parts = append(parts, fmt.Sprintf("deferred_leaf_mismatches=%d", len(safety.DeferredLeafMismatches)))
		}
	}
	return " " + strings.Join(parts, " ")
}

func formatRelConvergences(items []RelConvergence) string {
	sorted := append([]RelConvergence(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Rel < sorted[j].Rel })
	out := make([]string, 0, fixedPointStallMaxListedItems)
	for i, item := range sorted {
		if i == fixedPointStallMaxListedItems {
			out = append(out, fmt.Sprintf("+%d", len(sorted)-fixedPointStallMaxListedItems))
			break
		}
		out = append(out, fmt.Sprintf("%s(%s:observed=%s,target=%s)",
			item.Rel, item.Reason, item.Observed.String(), item.Target.String()))
	}
	return "[" + strings.Join(out, ",") + "]"
}

func formatCPUSetMap(items map[string]machine.CPUSet) string {
	rels := make([]string, 0, len(items))
	for rel := range items {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	out := make([]string, 0, fixedPointStallMaxListedItems)
	for i, rel := range rels {
		if i == fixedPointStallMaxListedItems {
			out = append(out, fmt.Sprintf("+%d", len(rels)-fixedPointStallMaxListedItems))
			break
		}
		out = append(out, rel+"="+items[rel].String())
	}
	return "[" + strings.Join(out, ",") + "]"
}

func (e *FixedPointStallError) Unwrap() error { return ErrNoProgress }

// protectedPendingOverlapByDomain reports, per snapshot domain, which protected
// pending cpus the domain currently still holds. A protected cpu held by the
// reclaim domain is the signature of a pending scope that requires a reclaim
// shrink while the shrink is gated on the same protected set.
func protectedPendingOverlapByDomain(snap *CompleteSnapshot, protected machine.CPUSet) map[DomainID]machine.CPUSet {
	out := map[DomainID]machine.CPUSet{}
	if snap == nil || protected.IsEmpty() {
		return out
	}
	for id, union := range snap.DomainUnion {
		if overlap := union.Intersection(protected); !overlap.IsEmpty() {
			out[id] = overlap
		}
	}
	return out
}

func unmetTargetRels(snap *CompleteSnapshot, targets map[string]machine.CPUSet) []string {
	if snap == nil {
		return nil
	}
	out := make([]string, 0)
	for rel, target := range targets {
		entry, ok := snap.Entries[rel]
		if !ok || !entry.CPUs.Equals(target) {
			out = append(out, rel)
		}
	}
	sort.Strings(out)
	return out
}

func protectedTransferStallCPUs(
	plan PhasePlan,
	round *coordinatorRound,
	journal []AppliedPlanOperation,
) machine.CPUSet {
	if len(journal) != 0 {
		return machine.NewCPUSet()
	}
	if round == nil || round.dag == nil || len(plan.TransferGraph) == 0 {
		return machine.NewCPUSet()
	}
	protectedByDomain := protectedCPUSetByDomain(
		round.protectedByRel,
		round.protectedPending,
		round.dag,
	)
	all := machine.NewCPUSet()
	for source, destinations := range plan.TransferGraph {
		if !plan.DrainBatch[source].IsEmpty() {
			return machine.NewCPUSet()
		}
		for _, cpus := range destinations {
			if cpus.IsEmpty() {
				continue
			}
			if !cpus.IsSubsetOf(protectedByDomain[source]) {
				return machine.NewCPUSet()
			}
			all = all.Union(cpus)
		}
	}
	return all
}

// applyDrainPhases executes a drain plan one frontier batch at a time against
// the session, recording every non-empty batch as an ordered CompiledPhase.
// Projected and live adapters therefore share the same batch boundaries,
// rebasing, and admission-safety split.
func (r *coordinatorRound) applyDrainPhases(
	ctx context.Context,
	session phaseExecutionSession,
	plan PhasePlan,
	phases *[]CompiledPhase,
) (*CompleteSnapshot, map[DomainID]map[DomainID]machine.CPUSet, []AppliedPlanOperation, error) {
	fresh := plan.Base
	released := make(map[DomainID]map[DomainID]machine.CPUSet)
	journal := make([]AppliedPlanOperation, 0)
	if len(plan.Operations) == 0 {
		next, err := session.Snapshot(ctx)
		return next, released, journal, err
	}
	for len(plan.Operations) > 0 {
		batch, err := drainFrontier(plan)
		if err != nil {
			return fresh, released, journal, err
		}
		r.planID = batch.PlanID
		accumulateDrainTransfers(released, plan.TransferGraph, plan.DrainBatch)
		applyResult, err := session.Apply(ctx, batch)
		journal = append(journal, applyResult.Journal...)
		if err != nil {
			if recovered, snapshotErr := session.Snapshot(ctx); snapshotErr == nil {
				fresh = recovered
			}
			return fresh, released, journal, err
		}
		*phases = append(*phases, CompiledPhase{
			Kind:       PhaseDrain,
			Operations: append([]PlanOperation(nil), batch.Operations...),
		})
		next, err := session.Snapshot(ctx)
		if err != nil {
			return fresh, released, journal, err
		}
		fresh = next
		plan, err = rebaseDrainPlan(plan, fresh, r.dag, r.budget)
		if err != nil {
			return fresh, released, journal, err
		}
		if r.objective == ConvergenceObjectiveParentSafe {
			required, _, splitErr := SplitPlanForAdmission(&plan, AdmissionSafetyInput{
				PendingCPUSet:        r.admissionSafetyCPUSet(),
				PendingRequiredByRel: r.pendingRequiredByRel,
				DeferredCPUSetByRel:  r.deferredByRel,
				RequiredCPUSetByRel:  r.requiredByRel,
			})
			if splitErr != nil {
				return fresh, released, journal, splitErr
			}
			plan = *required
		}
	}
	return fresh, released, journal, nil
}

func (r *coordinatorRound) cloneForProjection() *coordinatorRound {
	out := *r
	out.targetByRel = model.CloneCPUSetMap(r.targetByRel)
	out.dynamicByRel = model.CloneCPUSetMap(r.dynamicByRel)
	out.deferredByRel = model.CloneCPUSetMap(r.deferredByRel)
	out.requiredByRel = model.CloneCPUSetMap(r.requiredByRel)
	out.pendingRequiredByRel = model.CloneCPUSetMap(r.pendingRequiredByRel)
	out.protectedPending = r.protectedPending.Clone()
	out.protectedByRel = model.CloneCPUSetMap(r.protectedByRel)
	out.requiredIdentityByRel = cloneIdentityMap(r.requiredIdentityByRel)
	out.expectedAbsentRels = cloneRelSet(r.expectedAbsentRels)
	out.cpuDetails = cloneCPUDetails(r.cpuDetails)
	out.reservedCPUs = r.reservedCPUs.Clone()
	out.witnesses = cloneReleaseWitnesses(r.witnesses)
	out.blocked = cloneDomainUnion(r.blocked)
	out.deferredCleanupRels = make(map[string]struct{})
	out.pendingSnapshot = CloneCompleteSnapshot(r.pendingSnapshot)
	out.round = 0
	if r.budget != nil {
		r.budget.mu.Lock()
		limit := r.budget.limit
		r.budget.mu.Unlock()
		out.budget = NewBudgetTracker(limit)
	}
	return &out
}

func cloneCPUDetails(in machine.CPUDetails) machine.CPUDetails {
	if in == nil {
		return nil
	}
	out := make(machine.CPUDetails, len(in))
	for cpu, info := range in {
		out[cpu] = info
	}
	return out
}

func cloneReleaseWitnesses(in []ReleaseWitness) []ReleaseWitness {
	out := append([]ReleaseWitness(nil), in...)
	for i := range out {
		out[i].CPUs = out[i].CPUs.Clone()
	}
	return out
}

// parentSafetySemanticTargets returns the per-rel cpuset targets ParentSafety
// must judge a converged (or converging) partition against.
//
// ParentSafety decides whether pending-protection CPUs sit inside the primary
// union and outside the reclaim union. The partition the trace actually drives
// the cgroup to is the effective/semantic target map: it folds pending
// protections onto the primary domain and deducts the same CPUs from reclaim.
// Judging that converged state against the raw DAG desired targets treats a
// freshly admitted pending CPU as simultaneously outside primary and inside
// reclaim, so the trace can never reach ParentSafe and admission stalls with
// ErrNoProgress. The live convergence loop already feeds ParentSafety
// round.semanticTargetByRel; the compiled admission trace (both the in-engine
// evaluation and the frozen re-evaluation) must use the same semantic targets.
// When a round has not materialized semantic targets, fall back to the raw DAG
// desired targets to preserve prior behavior.
func parentSafetySemanticTargets(r *coordinatorRound) map[string]machine.CPUSet {
	if r != nil && len(r.semanticTargetByRel) > 0 {
		return r.semanticTargetByRel
	}
	return desiredTargets(r.dag)
}

func freezeCoordinatorEvaluationInput(
	r *coordinatorRound,
	capabilities HierarchyCapabilities,
) FrozenCoordinatorEvaluationInput {
	return FrozenCoordinatorEvaluationInput{
		DAGSpecs:                nodeSpecsFromDAG(r.dag),
		TargetByRel:             model.CloneCPUSetMap(r.targetByRel),
		ParentSafetyTargetByRel: parentSafetySemanticTargets(r),
		TargetMemsByRel:         cloneStringMap(r.desiredMemsByRel()),
		DesiredByDomain:         cloneDomainUnion(r.desiredDomainUnion()),
		AllowedCPUs:             r.allowedCPUs().Clone(),
		ExpectedByRel:           model.CloneCPUSetMap(r.dynamicByRel),
		RequiredByRel:           model.CloneCPUSetMap(r.requiredByRel),
		DeferredByRel:           model.CloneCPUSetMap(r.deferredByRel),
		DeferredCleanupRels:     cloneRelSet(r.deferredCleanupRels),
		ProtectedPending:        r.admissionSafetyCPUSet(),
		PendingRequiredByRel:    model.CloneCPUSetMap(r.pendingRequiredByRel),
		Capabilities:            capabilities,
		AllowEmptyTarget:        r.allowEmptyTarget,
	}
}

func cloneFrozenCoordinatorEvaluationInput(
	in FrozenCoordinatorEvaluationInput,
) FrozenCoordinatorEvaluationInput {
	return FrozenCoordinatorEvaluationInput{
		DAGSpecs:                cloneNodeSpecs(in.DAGSpecs),
		TargetByRel:             model.CloneCPUSetMap(in.TargetByRel),
		ParentSafetyTargetByRel: model.CloneCPUSetMap(in.ParentSafetyTargetByRel),
		TargetMemsByRel:         cloneStringMap(in.TargetMemsByRel),
		DesiredByDomain:         cloneDomainUnion(in.DesiredByDomain),
		AllowedCPUs:             in.AllowedCPUs.Clone(),
		ExpectedByRel:           model.CloneCPUSetMap(in.ExpectedByRel),
		RequiredByRel:           model.CloneCPUSetMap(in.RequiredByRel),
		DeferredByRel:           model.CloneCPUSetMap(in.DeferredByRel),
		DeferredCleanupRels:     cloneRelSet(in.DeferredCleanupRels),
		ProtectedPending:        in.ProtectedPending.Clone(),
		PendingRequiredByRel:    model.CloneCPUSetMap(in.PendingRequiredByRel),
		Capabilities:            in.Capabilities,
		AllowEmptyTarget:        in.AllowEmptyTarget,
	}
}

func nodeSpecsFromDAG(dag *TopoDAG) []NodeSpec {
	if dag == nil {
		return nil
	}
	nodes := dag.Nodes()
	specs := make([]NodeSpec, 0, len(nodes))
	for _, node := range nodes {
		parentRel := ""
		if parent := parentNodeOf(node); parent != nil {
			parentRel = parent.Rel
		}
		specs = append(specs, NodeSpec{
			Rel:            node.Rel,
			Role:           node.Role,
			CPUs:           node.CPUs.Clone(),
			Mems:           node.Mems,
			ParentRel:      parentRel,
			Domain:         node.Domain,
			ControlledRoot: node.ControlledRoot,
			TrustAnchor:    node.TrustAnchor,
			Constraint: TopologyConstraint{
				CPUUpperBound: node.Constraint.CPUUpperBound.Clone(),
				MemUpperBound: node.Constraint.MemUpperBound.Clone(),
				Scope:         node.Constraint.Scope,
			},
			Metadata: cloneStringMap(node.Metadata),
		})
	}
	return specs
}

func cloneNodeSpecs(in []NodeSpec) []NodeSpec {
	out := make([]NodeSpec, len(in))
	for i, spec := range in {
		spec.CPUs = spec.CPUs.Clone()
		spec.Constraint.CPUUpperBound = spec.Constraint.CPUUpperBound.Clone()
		spec.Constraint.MemUpperBound = spec.Constraint.MemUpperBound.Clone()
		spec.Metadata = cloneStringMap(spec.Metadata)
		out[i] = spec
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Role != out[j].Role {
			return out[i].Role < out[j].Role
		}
		return out[i].Rel < out[j].Rel
	})
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func executionReservationCost(phases []CompiledPhase) ExecutionReservationCost {
	var forward PhysicalWriteCost
	var rollback PhysicalWriteCost
	for _, phase := range phases {
		for _, operation := range phase.Operations {
			forward = addPhysicalWriteCost(forward, physicalWriteCost(
				operation.ExpectedCurrent, operation.Target, operation.WriteMems))
			rollback = addPhysicalWriteCost(rollback, physicalWriteCost(
				operation.Target, operation.ExpectedCurrent, operation.WriteMems))
		}
	}
	return ExecutionReservationCost{Forward: forward, Rollback: rollback}
}

func cloneCPUSetTarget(in CPUSetTarget) CPUSetTarget {
	return CPUSetTarget{CPUs: in.CPUs.Clone(), Mems: in.Mems}
}

func cloneCPUSetTargetMap(in map[string]CPUSetTarget) map[string]CPUSetTarget {
	if in == nil {
		return nil
	}
	out := make(map[string]CPUSetTarget, len(in))
	for rel, target := range in {
		out[rel] = cloneCPUSetTarget(target)
	}
	return out
}

func cloneCompiledPhases(in []CompiledPhase) []CompiledPhase {
	out := make([]CompiledPhase, len(in))
	for phaseIndex, phase := range in {
		out[phaseIndex].Kind = phase.Kind
		out[phaseIndex].Operations = make([]PlanOperation, len(phase.Operations))
		for operationIndex, operation := range phase.Operations {
			operation.ExpectedChildUnion = operation.ExpectedChildUnion.Clone()
			operation.ExpectedCurrent.CPUs = operation.ExpectedCurrent.CPUs.Clone()
			operation.Target.CPUs = operation.Target.CPUs.Clone()
			out[phaseIndex].Operations[operationIndex] = operation
		}
	}
	return out
}

func cloneCoordinatorSnapshotEvaluation(in coordinatorSnapshotEvaluation) coordinatorSnapshotEvaluation {
	out := in
	out.Report.NonConvergedTargets = cloneRelConvergences(in.Report.NonConvergedTargets)
	out.Report.PendingToPrimary = in.Report.PendingToPrimary.Clone()
	out.Report.PendingToReclaim = in.Report.PendingToReclaim.Clone()
	out.Report.CleanupPendingPrimary = in.Report.CleanupPendingPrimary.Clone()
	out.Report.CleanupPendingReclaim = in.Report.CleanupPendingReclaim.Clone()
	out.ParentSafety.PendingOutsidePrimary = in.ParentSafety.PendingOutsidePrimary.Clone()
	out.ParentSafety.PendingInsideReclaim = in.ParentSafety.PendingInsideReclaim.Clone()
	out.ParentSafety.PrimaryReclaimOverlap = in.ParentSafety.PrimaryReclaimOverlap.Clone()
	out.ParentSafety.PendingScopeDeficit = model.CloneCPUSetMap(in.ParentSafety.PendingScopeDeficit)
	out.ParentSafety.RequiredFloorDeficit = model.CloneCPUSetMap(in.ParentSafety.RequiredFloorDeficit)
	out.ParentSafety.UnsafeRequiredRels = cloneRelConvergences(in.ParentSafety.UnsafeRequiredRels)
	out.ParentSafety.DeferredLeafMismatches = cloneRelConvergences(in.ParentSafety.DeferredLeafMismatches)
	return out
}

func cloneRelConvergences(in []RelConvergence) []RelConvergence {
	out := append([]RelConvergence(nil), in...)
	for i := range out {
		out[i].Observed = out[i].Observed.Clone()
		out[i].Target = out[i].Target.Clone()
	}
	return out
}

// FreezePhaseTrace validates and deep-copies a compiled trace, then binds the
// immutable copy to a deterministic content ID. This exported defensive
// boundary always treats its input as mutable and performs a fresh validation.
func FreezePhaseTrace(in *CompiledPhaseTrace) (*CompiledPhaseTrace, error) {
	return freezePhaseTrace(context.Background(), in)
}

// freezeValidatedPhaseTrace is the only constructor for the validated carrier.
// It defensively clones the raw trace, validates every compiler-derived
// invariant, and publishes no carrier on cancellation or validation failure.
func freezeValidatedPhaseTrace(
	ctx context.Context,
	in *CompiledPhaseTrace,
) (*validatedPhaseTrace, error) {
	frozen, err := freezePhaseTrace(ctx, in)
	if err != nil {
		return nil, err
	}
	return &validatedPhaseTrace{frozen: frozen}, nil
}

// freezePhaseTrace owns the defensive clone-and-validation pass shared by the
// public raw-trace API and the compiler-owned validated carrier boundary.
func freezePhaseTrace(
	ctx context.Context,
	in *CompiledPhaseTrace,
) (*CompiledPhaseTrace, error) {
	if in == nil {
		return nil, fmt.Errorf("cannot freeze nil phase trace")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	providedTraceID := in.TraceID
	out := *in
	out.TraceID = ""
	out.InitialSnapshot = CloneCompleteSnapshot(in.InitialSnapshot)
	out.CanonicalTargetByRel = cloneCPUSetTargetMap(in.CanonicalTargetByRel)
	out.RequiredCPUSetByRel = model.CloneCPUSetMap(in.RequiredCPUSetByRel)
	out.EvaluationInput = cloneFrozenCoordinatorEvaluationInput(in.EvaluationInput)
	out.FrozenBoundary = cloneFrozenBoundary(in.FrozenBoundary)
	out.RollbackRetirements = append(
		[]FrozenRollbackRetirement(nil), in.RollbackRetirements...)
	out.Phases = cloneCompiledPhases(in.Phases)
	out.FinalSnapshot = CloneCompleteSnapshot(in.FinalSnapshot)
	out.FinalEvaluation = cloneCoordinatorSnapshotEvaluation(in.FinalEvaluation)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateFrozenPhaseTrace(ctx, &out); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	evaluation, err := out.EvaluationInput.evaluate(out.FinalSnapshot)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	actualEvaluation := normalizeCoordinatorSnapshotEvaluation(out.FinalEvaluation)
	expectedEvaluation := normalizeCoordinatorSnapshotEvaluation(evaluation)
	if !reflect.DeepEqual(actualEvaluation, expectedEvaluation) {
		return nil, fmt.Errorf("frozen phase trace final evaluation does not match final snapshot evidence: got=%+v want=%+v",
			actualEvaluation, expectedEvaluation)
	}
	expectedBoundary, err := compileFrozenBoundaryV1(
		out.InitialSnapshot, out.EvaluationInput, out.Phases)
	if err != nil {
		return nil, fmt.Errorf("derive frozen phase trace boundary: %w", err)
	}
	if !frozenBoundariesEqual(out.FrozenBoundary, expectedBoundary) {
		return nil, fmt.Errorf("frozen phase trace boundary is not compiler-derived")
	}
	out.FinalEvaluation = expectedEvaluation
	out.TraceID = canonicalPhaseTraceID(&out)
	if providedTraceID != "" && providedTraceID != out.TraceID {
		return nil, fmt.Errorf("frozen phase trace identity does not match frozen inputs")
	}
	return &out, nil
}

// validateFrozenPhaseTrace checks the complete admission contract: snapshot
// integrity, objective satisfaction, operation replay, compiler-derived
// boundary and rollback authority, required-CPU final proof, and exact
// reservation cost. It performs no live hierarchy I/O.
func validateFrozenPhaseTrace(ctx context.Context, trace *CompiledPhaseTrace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if trace == nil || trace.InitialSnapshot == nil || trace.FinalSnapshot == nil {
		return fmt.Errorf("frozen phase trace requires initial and final snapshots")
	}
	if trace.ConvergenceID == "" {
		return fmt.Errorf("frozen phase trace requires convergence identity")
	}
	if trace.Objective != ConvergenceObjectiveFull && trace.Objective != ConvergenceObjectiveParentSafe {
		return fmt.Errorf("frozen phase trace has unsupported objective %q", trace.Objective)
	}
	if trace.InitialSnapshot.Capabilities != trace.Capabilities ||
		trace.FinalSnapshot.Capabilities != trace.Capabilities {
		return fmt.Errorf("frozen phase trace capabilities do not match snapshots")
	}
	if trace.EvaluationInput.Capabilities != trace.Capabilities {
		return fmt.Errorf("frozen phase trace evaluation capabilities do not match snapshots")
	}
	if len(trace.EvaluationInput.DAGSpecs) == 0 {
		return fmt.Errorf("frozen phase trace requires evaluation DAG semantics")
	}
	if err := validateFrozenBoundary(trace.FrozenBoundary, trace.InitialSnapshot); err != nil {
		return fmt.Errorf("frozen phase trace has invalid frozen boundary: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !reflect.DeepEqual(trace.RequiredCPUSetByRel, trace.EvaluationInput.RequiredByRel) {
		return fmt.Errorf("frozen phase trace required CPUs inputs disagree")
	}
	controlledRels := make(map[string]struct{}, len(trace.EvaluationInput.DAGSpecs))
	for _, spec := range trace.EvaluationInput.DAGSpecs {
		if err := ctx.Err(); err != nil {
			return err
		}
		controlledRels[spec.Rel] = struct{}{}
	}
	for rel := range trace.InitialSnapshot.UnavailableChildren {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, controlled := controlledRels[rel]; controlled {
			return fmt.Errorf("frozen phase trace initial snapshot marks controlled rel %q unavailable", rel)
		}
	}
	for rel := range trace.FinalSnapshot.UnavailableChildren {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, controlled := controlledRels[rel]; controlled {
			return fmt.Errorf("frozen phase trace final snapshot marks controlled rel %q unavailable", rel)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCompleteSnapshotEvidence(trace.InitialSnapshot); err != nil {
		return fmt.Errorf("frozen phase trace initial snapshot evidence is inconsistent: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateCompleteSnapshotEvidence(trace.FinalSnapshot); err != nil {
		return fmt.Errorf("frozen phase trace final snapshot evidence is inconsistent: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fingerprintSnapshot(trace.InitialSnapshot) != trace.InitialSnapshot.ID {
		return fmt.Errorf("frozen phase trace initial snapshot evidence is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if fingerprintSnapshot(trace.FinalSnapshot) != trace.FinalSnapshot.ID {
		return fmt.Errorf("frozen phase trace final snapshot evidence is invalid")
	}
	for rel, entry := range trace.InitialSnapshot.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Identity == (CgroupIdentity{}) {
			return fmt.Errorf("frozen phase trace initial snapshot rel %q is missing identity", rel)
		}
		for _, child := range trace.InitialSnapshot.Children[rel] {
			if err := ctx.Err(); err != nil {
				return err
			}
			if child.Identity == (CgroupIdentity{}) {
				return fmt.Errorf("frozen phase trace initial snapshot child %q/%q is missing identity", rel, child.Name)
			}
		}
	}
	for rel, entry := range trace.FinalSnapshot.Entries {
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Identity == (CgroupIdentity{}) {
			return fmt.Errorf("frozen phase trace final snapshot rel %q is missing identity", rel)
		}
		for _, child := range trace.FinalSnapshot.Children[rel] {
			if err := ctx.Err(); err != nil {
				return err
			}
			if child.Identity == (CgroupIdentity{}) {
				return fmt.Errorf("frozen phase trace final snapshot child %q/%q is missing identity", rel, child.Name)
			}
		}
	}
	if len(trace.Phases) == 0 && !traceObjectiveSatisfied(trace) {
		return fmt.Errorf("%w: empty trace does not satisfy objective %q", ErrNoProgress, trace.Objective)
	}
	if !traceObjectiveSatisfied(trace) {
		return fmt.Errorf("frozen phase trace final evaluation does not satisfy objective %q", trace.Objective)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTraceOperations(ctx, trace); err != nil {
		return err
	}
	expectedRollbackRetirements, err := compileFrozenRollbackRetirements(
		trace.InitialSnapshot, trace.FrozenBoundary, trace.Phases)
	if err != nil {
		return fmt.Errorf("derive frozen rollback retirements: %w", err)
	}
	if !reflect.DeepEqual(trace.RollbackRetirements, expectedRollbackRetirements) {
		return fmt.Errorf("frozen phase trace rollback retirements are not compiler-derived")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := compileFrozenGrowReleaseGuards(ctx, trace); err != nil {
		return fmt.Errorf("frozen phase trace grow release contract is invalid: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for rel, required := range trace.RequiredCPUSetByRel {
		if err := ctx.Err(); err != nil {
			return err
		}
		entry, ok := trace.FinalSnapshot.Entries[rel]
		if !ok || !required.IsSubsetOf(entry.CPUs) {
			return fmt.Errorf("frozen phase trace required CPUs for rel %q are absent from final physical proof", rel)
		}
	}
	expectedCost := executionReservationCost(trace.Phases)
	if trace.Cost != expectedCost {
		return fmt.Errorf("frozen phase trace cost mismatch: got=%+v want=%+v", trace.Cost, expectedCost)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func traceObjectiveSatisfied(trace *CompiledPhaseTrace) bool {
	if trace.Objective == ConvergenceObjectiveParentSafe {
		return trace.FinalEvaluation.ParentSafety.Safe
	}
	return trace.FinalEvaluation.Report.FullyConverged
}

// validateCompleteSnapshotEvidence proves the snapshot's structural closure:
// entries and domains are bijective, every listed child has exactly one
// identity-matching entry or typed-unavailable proof, every root and expanded
// rel has its required evidence, and domain unions equal the closed entry set.
func validateCompleteSnapshotEvidence(snapshot *CompleteSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("snapshot is nil")
	}
	if err := validateUnavailableChildEvidence(snapshot); err != nil {
		return err
	}
	for rel, entry := range snapshot.Entries {
		if entry.Rel != rel {
			return fmt.Errorf("entry key %q disagrees with entry rel %q", rel, entry.Rel)
		}
		if _, ok := snapshot.DomainByRel[rel]; !ok {
			return fmt.Errorf("entry %q has no domain", rel)
		}
	}
	for rel := range snapshot.DomainByRel {
		if _, ok := snapshot.Entries[rel]; !ok {
			return fmt.Errorf("domain rel %q has no entry", rel)
		}
	}
	for rel := range snapshot.Children {
		if _, ok := snapshot.Entries[rel]; !ok {
			return fmt.Errorf("children parent %q has no entry", rel)
		}
	}
	for parentRel, children := range snapshot.Children {
		names := make(map[string]struct{}, len(children))
		for _, child := range children {
			if _, duplicate := names[child.Name]; duplicate {
				return fmt.Errorf("children parent %q has duplicate child %q", parentRel, child.Name)
			}
			names[child.Name] = struct{}{}
			childRel := filepath.Join(parentRel, child.Name)
			entry, hasEntry := snapshot.Entries[childRel]
			unavailable, hasUnavailable := snapshot.UnavailableChildren[childRel]
			if hasEntry == hasUnavailable {
				return fmt.Errorf("child %q must have exactly one entry or unavailable proof", childRel)
			}
			if hasEntry && entry.Identity != child.Identity {
				return fmt.Errorf("child %q identity disagrees with entry", childRel)
			}
			if hasUnavailable && unavailable.Identity != child.Identity {
				return fmt.Errorf("child %q identity disagrees with unavailable proof", childRel)
			}
		}
	}
	for _, rel := range snapshot.ScanBoundary.Roots {
		if _, ok := snapshot.Entries[rel]; !ok {
			return fmt.Errorf("root %q has no entry", rel)
		}
	}
	expanded := make(map[string]struct{}, len(snapshot.ScanBoundary.ExpandedRels))
	for _, rel := range snapshot.ScanBoundary.ExpandedRels {
		if _, duplicate := expanded[rel]; duplicate {
			return fmt.Errorf("expanded rel %q is duplicated", rel)
		}
		expanded[rel] = struct{}{}
		if _, ok := snapshot.Entries[rel]; !ok {
			return fmt.Errorf("expanded rel %q has no entry", rel)
		}
		if _, ok := snapshot.Children[rel]; !ok {
			return fmt.Errorf("expanded rel %q has no children evidence", rel)
		}
	}
	union := make(map[DomainID]machine.CPUSet)
	for rel, entry := range snapshot.Entries {
		domain := snapshot.DomainByRel[rel]
		union[domain] = union[domain].Union(entry.CPUs)
	}
	if len(union) != len(snapshot.DomainUnion) {
		return fmt.Errorf("domain union keys do not match entry domains")
	}
	for domain, cpus := range union {
		if observed, ok := snapshot.DomainUnion[domain]; !ok || !observed.Equals(cpus) {
			return fmt.Errorf("domain union %q does not match entries", domain)
		}
	}
	return nil
}

func validateUnavailableChildEvidence(snapshot *CompleteSnapshot) error {
	if len(snapshot.UnavailableChildren) == 0 {
		return nil
	}
	if !snapshot.Capabilities.EffectiveCPUSet || !snapshot.Capabilities.StableIdentity {
		return fmt.Errorf("unavailable-child evidence requires effective cpuset and stable identity capabilities")
	}
	for rel, evidence := range snapshot.UnavailableChildren {
		if evidence.Reason != UnavailableChildReasonControllerUnavailable {
			return fmt.Errorf("unavailable child %q has unsupported skip reason %q", rel, evidence.Reason)
		}
		if evidence.Identity == (CgroupIdentity{}) {
			return fmt.Errorf("unavailable child %q is missing identity", rel)
		}
		if _, exists := snapshot.Entries[rel]; exists {
			return fmt.Errorf("unavailable child %q also has entry evidence", rel)
		}
		parentRel := filepath.Dir(rel)
		if parentRel == "." {
			parentRel = ""
		}
		name := filepath.Base(rel)
		matched := false
		for _, child := range snapshot.Children[parentRel] {
			if child.Name != name {
				continue
			}
			if child.Identity != evidence.Identity {
				return fmt.Errorf("unavailable child %q identity disagrees with parent listing", rel)
			}
			matched = true
			break
		}
		if !matched {
			return fmt.Errorf("unavailable child %q has no matching parent listing", rel)
		}
	}
	return nil
}

func normalizeCoordinatorSnapshotEvaluation(in coordinatorSnapshotEvaluation) coordinatorSnapshotEvaluation {
	out := cloneCoordinatorSnapshotEvaluation(in)
	if out.ParentSafety.RequiredFloorDeficit == nil {
		out.ParentSafety.RequiredFloorDeficit = make(map[string]machine.CPUSet)
	}
	if out.ParentSafety.PendingScopeDeficit == nil {
		out.ParentSafety.PendingScopeDeficit = make(map[string]machine.CPUSet)
	}
	sortRelConvergences(out.Report.NonConvergedTargets)
	sortRelConvergences(out.ParentSafety.UnsafeRequiredRels)
	sortRelConvergences(out.ParentSafety.DeferredLeafMismatches)
	return out
}

func sortRelConvergences(values []RelConvergence) {
	sort.Slice(values, func(i, j int) bool {
		left, right := values[i], values[j]
		if left.Rel != right.Rel {
			return left.Rel < right.Rel
		}
		if left.Reason != right.Reason {
			return left.Reason < right.Reason
		}
		if left.Observed.String() != right.Observed.String() {
			return left.Observed.String() < right.Observed.String()
		}
		return left.Target.String() < right.Target.String()
	})
}

func validateTraceOperations(ctx context.Context, trace *CompiledPhaseTrace) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	projection, err := newProjectedHierarchy(trace.InitialSnapshot, trace.Capabilities)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	session := &projectedPhaseSession{
		hierarchy: projection,
		progress:  make(map[phaseProgressKey]struct{}),
	}
	for phaseIndex, phase := range trace.Phases {
		if err := ctx.Err(); err != nil {
			return err
		}
		if phase.Kind != PhaseDrain && phase.Kind != PhaseExpand {
			return fmt.Errorf("frozen phase trace has invalid phase %q at index %d", phase.Kind, phaseIndex)
		}
		for operationIndex, operation := range phase.Operations {
			if err := ctx.Err(); err != nil {
				return err
			}
			entry, ok := projection.snapshot.Entries[operation.Rel]
			if !ok {
				return fmt.Errorf("frozen phase trace operation %d/%d refers to unknown rel %q",
					phaseIndex, operationIndex, operation.Rel)
			}
			if operation.ExpectedIdentity == (CgroupIdentity{}) ||
				entry.Identity != operation.ExpectedIdentity {
				return fmt.Errorf("frozen phase trace operation %d/%d identity mismatch for rel %q",
					phaseIndex, operationIndex, operation.Rel)
			}
			if operation.ParentRel != "" {
				parent, ok := projection.snapshot.Entries[operation.ParentRel]
				if !ok || operation.ExpectedParentIdentity == (CgroupIdentity{}) ||
					parent.Identity != operation.ExpectedParentIdentity {
					return fmt.Errorf("frozen phase trace operation %d/%d parent identity mismatch for rel %q",
						phaseIndex, operationIndex, operation.Rel)
				}
			}
			if got := ChildrenFingerprint(projection.snapshot.Children[operation.Rel]); got != operation.ExpectedChildren {
				return fmt.Errorf("frozen phase trace operation %d/%d child fingerprint mismatch for rel %q",
					phaseIndex, operationIndex, operation.Rel)
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		planID := ""
		if len(phase.Operations) > 0 {
			planID = phase.Operations[0].PlanID
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := session.Apply(ctx, PhasePlan{
			PlanID: planID, Kind: phase.Kind,
			Operations: append([]PlanOperation(nil), phase.Operations...),
		})
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if err != nil {
			return fmt.Errorf("validate frozen phase trace operation frontier=%d: %w", phaseIndex, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if projection.snapshot.ID != trace.FinalSnapshot.ID {
		return fmt.Errorf("frozen phase trace projected final snapshot mismatch")
	}
	return nil
}

// compileFrozenRollbackRetirements derives the complete retirement authority
// from immutable compile-time evidence. Only physically written dynamic
// holders that terminate frozen relevant-holder paths, are neither controlled
// nodes nor roots, and match the exact initial generation are eligible. Empty
// authority is canonicalized to nil so trace identity remains stable.
func compileFrozenRollbackRetirements(
	initial *CompleteSnapshot,
	boundary FrozenBoundary,
	phases []CompiledPhase,
) ([]FrozenRollbackRetirement, error) {
	if initial == nil {
		return nil, fmt.Errorf("rollback retirement contract requires initial snapshot")
	}
	controlled := make(map[string]struct{}, len(boundary.ControlledRels))
	for _, rel := range boundary.ControlledRels {
		controlled[rel] = struct{}{}
	}
	roots := make(map[string]struct{}, len(boundary.Roots))
	for _, rel := range boundary.Roots {
		roots[rel] = struct{}{}
	}
	dynamicIdentities := make(map[string]CgroupIdentity, len(boundary.RelevantCPUHolders))
	for _, rel := range boundary.RelevantCPUHolders {
		if _, isControlled := controlled[rel]; isControlled {
			continue
		}
		if _, isRoot := roots[rel]; isRoot {
			continue
		}
		path := boundary.RelevantHolderPaths[rel]
		if len(path) < 2 || path[len(path)-1].Rel != rel {
			continue
		}
		dynamicIdentities[rel] = path[len(path)-1].Identity
	}
	identities := make(map[string]CgroupIdentity)
	for _, phase := range phases {
		for _, operation := range phase.Operations {
			writesCPUs := !operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs)
			writesMems := operation.WriteMems &&
				operation.ExpectedCurrent.Mems != operation.Target.Mems
			if !writesCPUs && !writesMems {
				continue
			}
			frozenIdentity, isDynamic := dynamicIdentities[operation.Rel]
			if !isDynamic {
				continue
			}
			entry, ok := initial.Entries[operation.Rel]
			if !ok {
				return nil, fmt.Errorf(
					"rollback retirement rel %q has no initial snapshot entry",
					operation.Rel)
			}
			if operation.ExpectedIdentity == (CgroupIdentity{}) ||
				entry.Identity != operation.ExpectedIdentity ||
				frozenIdentity != operation.ExpectedIdentity {
				return nil, fmt.Errorf(
					"rollback retirement rel %q identity does not match frozen boundary",
					operation.Rel)
			}
			if identity, exists := identities[operation.Rel]; exists &&
				identity != operation.ExpectedIdentity {
				return nil, fmt.Errorf(
					"rollback retirement rel %q has conflicting identities",
					operation.Rel)
			}
			identities[operation.Rel] = operation.ExpectedIdentity
		}
	}
	rels := sortedStringKeys(identities)
	if len(rels) == 0 {
		return nil, nil
	}
	out := make([]FrozenRollbackRetirement, 0, len(rels))
	for _, rel := range rels {
		out = append(out, FrozenRollbackRetirement{
			Rel: rel, Identity: identities[rel],
		})
	}
	return out, nil
}

func canonicalPhaseTraceID(trace *CompiledPhaseTrace) string {
	hash := sha256.New()
	writeHashString(hash, "bulkhead-cpuset-phase-trace-v2")
	writeHashString(hash, trace.ConvergenceID)
	writeHashString(hash, string(trace.Objective))
	_, _ = hash.Write(trace.InitialSnapshot.ID[:])
	writeHierarchyCapabilitiesHash(hash, trace.Capabilities)
	writeCPUSetTargetMapHash(hash, trace.CanonicalTargetByRel)
	writeCPUSetMapHash(hash, trace.RequiredCPUSetByRel)
	writeFrozenCoordinatorEvaluationInputHash(hash, trace.EvaluationInput)
	writeFrozenBoundaryHash(hash, trace.FrozenBoundary)
	writeHashUint64(hash, uint64(len(trace.RollbackRetirements)))
	for _, retirement := range trace.RollbackRetirements {
		writeHashString(hash, retirement.Rel)
		writeHashUint64(hash, retirement.Identity.Device)
		writeHashUint64(hash, retirement.Identity.Inode)
	}
	writeHashUint64(hash, uint64(len(trace.Phases)))
	for _, phase := range trace.Phases {
		writeHashString(hash, string(phase.Kind))
		writeHashUint64(hash, uint64(len(phase.Operations)))
		for _, operation := range phase.Operations {
			writePlanOperationHash(hash, operation)
		}
	}
	_, _ = hash.Write(trace.FinalSnapshot.ID[:])
	writeCoordinatorSnapshotEvaluationHash(hash, normalizeCoordinatorSnapshotEvaluation(trace.FinalEvaluation))
	writeHashUint64(hash, uint64(trace.Cost.Forward.CPUSetWrites))
	writeHashUint64(hash, uint64(trace.Cost.Forward.MemsWrites))
	writeHashUint64(hash, uint64(trace.Cost.Rollback.CPUSetWrites))
	writeHashUint64(hash, uint64(trace.Cost.Rollback.MemsWrites))
	return fmt.Sprintf("%x", hash.Sum(nil))
}

func writeFrozenCoordinatorEvaluationInputHash(
	hash interface{ Write([]byte) (int, error) },
	in FrozenCoordinatorEvaluationInput,
) {
	writeHashString(hash, "coordinator-evaluation-input-v1")
	writeHashUint64(hash, uint64(len(in.DAGSpecs)))
	for _, spec := range in.DAGSpecs {
		writeHashString(hash, spec.Rel)
		writeHashString(hash, string(spec.Role))
		writeHashString(hash, spec.CPUs.String())
		writeHashString(hash, spec.Mems)
		writeHashString(hash, spec.ParentRel)
		writeHashString(hash, string(spec.Domain))
		writeHashUint64(hash, boolUint64(spec.ControlledRoot))
		writeHashUint64(hash, boolUint64(spec.TrustAnchor))
		writeHashString(hash, spec.Constraint.CPUUpperBound.String())
		writeHashString(hash, spec.Constraint.MemUpperBound.String())
		writeHashString(hash, string(spec.Constraint.Scope))
		writeStringMapHash(hash, spec.Metadata)
	}
	writeCPUSetMapHash(hash, in.TargetByRel)
	writeCPUSetMapHash(hash, in.ParentSafetyTargetByRel)
	writeStringMapHash(hash, in.TargetMemsByRel)
	writeDomainCPUSetMapHash(hash, in.DesiredByDomain)
	writeHashString(hash, in.AllowedCPUs.String())
	writeCPUSetMapHash(hash, in.ExpectedByRel)
	writeCPUSetMapHash(hash, in.RequiredByRel)
	writeCPUSetMapHash(hash, in.DeferredByRel)
	writeRelSetHash(hash, in.DeferredCleanupRels)
	writeHashString(hash, in.ProtectedPending.String())
	writeCPUSetMapHash(hash, in.PendingRequiredByRel)
	writeHashUint64(hash, hierarchyCapabilitiesBits(in.Capabilities))
	writeHashUint64(hash, boolUint64(in.AllowEmptyTarget))
}

func writeCoordinatorSnapshotEvaluationHash(
	hash interface{ Write([]byte) (int, error) },
	evaluation coordinatorSnapshotEvaluation,
) {
	writeHashString(hash, "final-evaluation")
	writeHashUint64(hash, boolUint64(evaluation.Report.FullyConverged))
	writeRelConvergencesHash(hash, evaluation.Report.NonConvergedTargets)
	writeHashString(hash, evaluation.Report.PendingToPrimary.String())
	writeHashString(hash, evaluation.Report.PendingToReclaim.String())
	writeHashString(hash, evaluation.Report.CleanupPendingPrimary.String())
	writeHashString(hash, evaluation.Report.CleanupPendingReclaim.String())
	writeHashUint64(hash, boolUint64(evaluation.ParentSafety.Safe))
	writeHashString(hash, evaluation.ParentSafety.PendingOutsidePrimary.String())
	writeHashString(hash, evaluation.ParentSafety.PendingInsideReclaim.String())
	writeHashString(hash, evaluation.ParentSafety.PrimaryReclaimOverlap.String())
	writeCPUSetMapHash(hash, evaluation.ParentSafety.PendingScopeDeficit)
	writeCPUSetMapHash(hash, evaluation.ParentSafety.RequiredFloorDeficit)
	writeRelConvergencesHash(hash, evaluation.ParentSafety.UnsafeRequiredRels)
	writeRelConvergencesHash(hash, evaluation.ParentSafety.DeferredLeafMismatches)
}

func writeRelConvergencesHash(
	hash interface{ Write([]byte) (int, error) },
	values []RelConvergence,
) {
	writeHashUint64(hash, uint64(len(values)))
	for _, value := range values {
		writeHashString(hash, value.Rel)
		writeHashString(hash, value.Observed.String())
		writeHashString(hash, value.Target.String())
		writeHashString(hash, value.ObservedMems)
		writeHashString(hash, value.TargetMems)
		writeHashString(hash, value.Reason)
	}
}

func writeCPUSetTargetMapHash(
	hash interface{ Write([]byte) (int, error) },
	values map[string]CPUSetTarget,
) {
	keys := sortedStringKeys(values)
	writeHashUint64(hash, uint64(len(keys)))
	for _, rel := range keys {
		target := values[rel]
		writeHashString(hash, rel)
		writeHashString(hash, target.CPUs.String())
		writeHashString(hash, target.Mems)
	}
}

func writeCPUSetMapHash(
	hash interface{ Write([]byte) (int, error) },
	values map[string]machine.CPUSet,
) {
	keys := sortedStringKeys(values)
	writeHashUint64(hash, uint64(len(keys)))
	for _, rel := range keys {
		writeHashString(hash, rel)
		writeHashString(hash, values[rel].String())
	}
}

func writeStringMapHash(
	hash interface{ Write([]byte) (int, error) },
	values map[string]string,
) {
	keys := sortedStringKeys(values)
	writeHashUint64(hash, uint64(len(keys)))
	for _, key := range keys {
		writeHashString(hash, key)
		writeHashString(hash, values[key])
	}
}

func writeDomainCPUSetMapHash(
	hash interface{ Write([]byte) (int, error) },
	values map[DomainID]machine.CPUSet,
) {
	keys := make([]DomainID, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	writeHashUint64(hash, uint64(len(keys)))
	for _, key := range keys {
		writeHashString(hash, string(key))
		writeHashString(hash, values[key].String())
	}
}

func writeRelSetHash(
	hash interface{ Write([]byte) (int, error) },
	values map[string]struct{},
) {
	keys := sortedStringKeys(values)
	writeHashUint64(hash, uint64(len(keys)))
	for _, key := range keys {
		writeHashString(hash, key)
	}
}

func sortedStringKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func writePlanOperationHash(
	hash interface{ Write([]byte) (int, error) },
	operation PlanOperation,
) {
	writeHashString(hash, operation.PlanID)
	writeHashString(hash, operation.Rel)
	writeHashUint64(hash, operation.ExpectedIdentity.Device)
	writeHashUint64(hash, operation.ExpectedIdentity.Inode)
	writeHashString(hash, operation.ExpectedChildren)
	writeHashString(hash, operation.ExpectedChildUnion.String())
	writeHashString(hash, operation.ParentRel)
	writeHashUint64(hash, operation.ExpectedParentIdentity.Device)
	writeHashUint64(hash, operation.ExpectedParentIdentity.Inode)
	writeHashString(hash, operation.ExpectedCurrent.CPUs.String())
	writeHashString(hash, operation.ExpectedCurrent.Mems)
	writeHashString(hash, operation.Target.CPUs.String())
	writeHashString(hash, operation.Target.Mems)
	writeHashString(hash, string(operation.Direction))
	writeHashUint64(hash, boolUint64(operation.OwnsMems))
	writeHashUint64(hash, boolUint64(operation.WriteMems))
	writeHashString(hash, string(operation.Requirement))
}
