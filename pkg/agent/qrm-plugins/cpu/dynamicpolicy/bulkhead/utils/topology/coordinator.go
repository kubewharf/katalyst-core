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
	"sort"
	"strings"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/deadlock"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	// Auto mode retains retry/replan headroom for small workloads while scaling
	// above it when the drain workload requires more rounds.
	defaultCoordinatorAutoRounds = 32
	// One completion round covers phase handoffs that become drainable only
	// after the last workload-derived transfer round.
	coordinatorPhaseCompletionRounds = 1
)

var ErrCoordinatorPlanStale = errors.New("coordinator plan is stale and requires replan")

type PlanStaleError struct {
	Rel       string
	Direction WriteDirection
	Resource  string
	Current   string
	Target    string
	Err       error
}

func (e *PlanStaleError) Error() string {
	message := fmt.Sprintf("%v: rel=%q direction=%s resource=%s current=%s target=%s",
		ErrCoordinatorPlanStale, e.Rel, e.Direction, e.Resource, e.Current, e.Target)
	if e.Err != nil {
		message += ": " + e.Err.Error()
	}
	return message
}

func (e *PlanStaleError) Unwrap() error { return e.Err }

func (e *PlanStaleError) Is(target error) bool { return target == ErrCoordinatorPlanStale }

func (e *PlanStaleError) ReplanRequired() bool { return true }

type CoordinatorBlockedError struct {
	Blocker error
}

func (e *CoordinatorBlockedError) Error() string {
	return fmt.Sprintf("topology coordinator blocked after repeated no-progress rounds: %v", e.Blocker)
}

func (e *CoordinatorBlockedError) Unwrap() error { return e.Blocker }

type CoordinatorMode string

const (
	CoordinatorModeNormal CoordinatorMode = "normal"
	CoordinatorModeReset  CoordinatorMode = "reset"
)

type ModeGuard struct {
	mode CoordinatorMode
	gate *ModeGate
}

func NormalModeGuard() ModeGuard { return ModeGuard{mode: CoordinatorModeNormal} }

func ResetModeGuard() ModeGuard { return ModeGuard{mode: CoordinatorModeReset} }

func NormalModeGuardWithGate(gate *ModeGate) ModeGuard {
	return ModeGuard{mode: CoordinatorModeNormal, gate: gate}
}

func ResetModeGuardWithGate(gate *ModeGate) ModeGuard {
	return ModeGuard{mode: CoordinatorModeReset, gate: gate}
}

func (g ModeGuard) modeOrDefault() CoordinatorMode {
	if g.mode == "" {
		return CoordinatorModeNormal
	}
	return g.mode
}

func (g ModeGuard) validate() error {
	switch g.modeOrDefault() {
	case CoordinatorModeNormal, CoordinatorModeReset:
		return nil
	default:
		return fmt.Errorf("unsupported coordinator mode %q", g.mode)
	}
}

type ModeGate struct {
	mu     sync.Mutex
	active CoordinatorMode
	held   bool
}

func NewModeGate() *ModeGate { return &ModeGate{} }

type ModeToken struct {
	gate *ModeGate
	mode CoordinatorMode
}

func (t ModeToken) Exit() {
	if t.gate == nil {
		return
	}
	t.gate.exit(t.mode)
}

type CoordinatorBusyError struct {
	Requested CoordinatorMode
	Active    CoordinatorMode
}

func (e *CoordinatorBusyError) Error() string {
	return fmt.Sprintf("topology coordinator busy: requested=%s active=%s", e.Requested, e.Active)
}

func (g ModeGuard) TryEnter() (ModeToken, error) {
	mode := g.modeOrDefault()
	if err := g.validate(); err != nil {
		return ModeToken{}, err
	}
	if g.gate == nil {
		return ModeToken{mode: mode}, nil
	}
	return g.gate.tryEnter(mode)
}

func (g *ModeGate) tryEnter(mode CoordinatorMode) (ModeToken, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.held {
		return ModeToken{}, &CoordinatorBusyError{Requested: mode, Active: g.active}
	}
	g.held = true
	g.active = mode
	return ModeToken{gate: g, mode: mode}, nil
}

func (g *ModeGate) exit(mode CoordinatorMode) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.held || g.active != mode {
		return
	}
	g.held = false
	g.active = ""
}

type ConvergenceState string

const (
	ConvergenceStateConverged              ConvergenceState = "converged"
	ConvergenceStateParentSafeLeafDeferred ConvergenceState = "parent_safe_leaf_deferred"
	ConvergenceStateNonConverged           ConvergenceState = "non_converged"
	ConvergenceStateBlocked                ConvergenceState = "blocked"
)

type ConvergenceObjective string

const (
	ConvergenceObjectiveFull       ConvergenceObjective = "full"
	ConvergenceObjectiveParentSafe ConvergenceObjective = "parent_safe"
)

func (o ConvergenceObjective) orFullDefault() ConvergenceObjective {
	if o == "" {
		return ConvergenceObjectiveFull
	}
	return o
}

type AdmissionConvergenceBudget struct {
	MaxRequiredWrites int
}

type ConvergenceResult struct {
	Attempted int
	Applied   int
	Skipped   int
	Failed    int
	// Deferred counts controlled nodes whose final generational shrink could not
	// land this pass but left the parent as a valid cgroup v1 superset. These are
	// not failures: the next periodical reconcile finishes the shrink. A non-zero
	// Deferred implies Converged is false.
	Deferred          int
	Converged         bool
	State             ConvergenceState
	ConvergenceReport ConvergenceReport
	Journal           []AppliedPlanOperation
	Rounds            []RoundOutcome
	FinalSnapshot     *CompleteSnapshot
	// FinalSnapshotCurrent is true only when FinalSnapshot is the snapshot used
	// for the successful convergence decision and no later hierarchy write ran.
	FinalSnapshotCurrent  bool
	ParentSafe            bool
	DeferredLeafCount     int
	DeferredCPUCount      int
	ReplanDisposition     ReplanDisposition
	Published             bool
	forwardWriteAttempts  int
	rollbackWriteAttempts int
	publicationAttempted  bool
}

func (r *ConvergenceResult) recordForwardWriteAttempt() {
	if r != nil {
		r.forwardWriteAttempts = saturatingAdd(r.forwardWriteAttempts, 1)
	}
}

func (r *ConvergenceResult) recordRollbackWriteAttempt() {
	if r != nil {
		r.rollbackWriteAttempts = saturatingAdd(r.rollbackWriteAttempts, 1)
	}
}

func (r ConvergenceResult) hasPhysicalWriteEvidence() bool {
	return r.forwardWriteAttempts != 0 || r.rollbackWriteAttempts != 0
}

func (r ConvergenceResult) FirstBlocker() string {
	for _, round := range r.Rounds {
		if round.Blocker != nil {
			return round.Blocker.Error()
		}
	}
	if len(r.ConvergenceReport.NonConvergedTargets) > 0 {
		mismatch := r.ConvergenceReport.NonConvergedTargets[0]
		return fmt.Sprintf("rel=%q observed=%s target=%s observed_mems=%q target_mems=%q reason=%s",
			mismatch.Rel, mismatch.Observed.String(), mismatch.Target.String(),
			mismatch.ObservedMems, mismatch.TargetMems, mismatch.Reason)
	}
	return "none"
}

type CoordinatorInput struct {
	DAG    *TopoDAG
	Cgroup cgroupclient.CgroupClient
	Mems   string
	Mode   ModeGuard

	CPUDetails     machine.CPUDetails
	ReservedCPUSet machine.CPUSet
	// InitialSnapshot may seed a recompile only when the preceding coordinator
	// returned ReplanSafeFromVerifiedFinalState. The caller passes a deep copy
	// of that complete, current physical snapshot.
	InitialSnapshot     *CompleteSnapshot
	ExpectedCPUSetByRel map[string]machine.CPUSet
	RequiredCPUSetByRel map[string]machine.CPUSet
	// PendingProtections is the canonical owner of pending allocations. The
	// coordinator derives both their union and controlled ancestor closure.
	PendingProtections []PendingProtection
	// ProtectedCPUSetByRel records cgroup rels whose current/pending cpuset must
	// stay covered during a short runtime creation window.
	ProtectedCPUSetByRel map[string]machine.CPUSet
	// TraversalBoundaries are normalized cgroup rels that reset propagation
	// must neither write nor descend into.
	TraversalBoundaries map[string]struct{}
	// RequiredIdentityByRel binds preflight classification to every coordinator
	// snapshot before that snapshot can authorize a hierarchy write or replan.
	RequiredIdentityByRel map[string]CgroupIdentity
	// ExpectedAbsentRels binds preflight absence classification to every
	// coordinator snapshot before that snapshot can authorize a hierarchy write.
	ExpectedAbsentRels  map[string]struct{}
	Objective           ConvergenceObjective
	DeferredCPUSetByRel map[string]machine.CPUSet
	AdmissionBudget     *AdmissionConvergenceBudget

	Budget ConvergenceBudget
	// AdjustmentBudget is shared by every coordinator invocation belonging to
	// one plugin adjustment and reserves physical forward plus rollback writes.
	AdjustmentBudget *AdjustmentBudget
	DrainSelection   DrainSelectionPolicy
	// PublishFinalSnapshot runs while both the coordinator mode gate and the
	// caller's manager mutex are still held. The coordinator invokes it only
	// after a fresh complete snapshot has the same publish-relevant controlled
	// and explicit-leaf state as the snapshot that proved convergence.
	PublishFinalSnapshot func(*CompleteSnapshot) error
	// PublishParentSafeSnapshot publishes a partition-safe view while exact
	// cleanup for the supplied rels remains deferred.
	PublishParentSafeSnapshot func(*CompleteSnapshot, map[string]struct{}) error
}

type TopologyCoordinator struct{}

// Converge runs one topology adjustment pass to bring controlled cgroup
// directories to the objective.
//
// Concurrency model:
//   - Single writer per mode: in.Mode.TryEnter takes the shared ModeGate token
//     before any cgroup write. A second overlapping Converge call (same or the
//     opposite mode) fails fast with CoordinatorBusyError instead of contending,
//     so at most one pass mutates the cgroup hierarchy at a time. The token is
//     released by the deferred Exit.
//   - Generation/fence: the pass snapshots the hierarchy up front and refuses to
//     apply a plan against a stale view (SnapshotError/HierarchyErrorStale): the
//     result is downgraded to non-converged and the caller replans, rather than
//     writing an out-of-date cpuset over a newer external change.
//   - Lock granularity: there is no coarse global lock. Mutual exclusion is only
//     the mode token; budgets, snapshots and the journal are per-invocation, so a
//     blocked or slow pass cannot pin unrelated readers.
func (c TopologyCoordinator) Converge(ctx context.Context, in CoordinatorInput) (ConvergenceResult, error) {
	res := ConvergenceResult{
		ReplanDisposition: ReplanNotAllowed,
		Published:         false,
	}
	if in.DAG == nil {
		return res, errors.New("TopologyCoordinator.Converge: nil DAG")
	}
	if in.Cgroup == nil {
		return res, errors.New("TopologyCoordinator.Converge: nil Cgroup client")
	}
	if err := in.Mode.validate(); err != nil {
		return res, err
	}
	switch in.Objective.orFullDefault() {
	case ConvergenceObjectiveFull, ConvergenceObjectiveParentSafe:
	default:
		return res, fmt.Errorf("unsupported convergence objective %q", in.Objective)
	}
	token, err := in.Mode.TryEnter()
	if err != nil {
		return res, err
	}
	defer token.Exit()
	budgetLimit := BudgetWithInvocationDeadline(ctx, in.Budget, time.Now())
	budget := NewBudgetTracker(budgetLimit)
	allowEmptyTarget := in.Cgroup.Version(ctx) == cgroupclient.CgroupVersionV2
	collectDormantActivity := in.Mode.modeOrDefault() == CoordinatorModeNormal && !allowEmptyTarget
	initialSnapshotRetryAllowance := budgetLimit.MaxRounds
	if initialSnapshotRetryAllowance == 0 {
		initialSnapshotRetryAllowance = defaultCoordinatorAutoRounds
	}
	if err := budget.configureAutoHierarchyIOBootstrap(initialSnapshotRetryAllowance, collectDormantActivity); err != nil {
		return res, err
	}
	var result ConvergenceResult
	switch in.Mode.modeOrDefault() {
	case CoordinatorModeReset:
		result, err = c.convergeReset(ctx, in, &res, budget)
	case CoordinatorModeNormal:
		result, err = c.convergeNormal(ctx, in, &res, budget)
	default:
		return res, fmt.Errorf("unsupported coordinator mode %q", in.Mode.modeOrDefault())
	}
	successfulCurrentProof := result.FinalSnapshotCurrent &&
		result.FinalSnapshot != nil &&
		(result.Converged || result.ParentSafe)
	if deadlineErr := ctx.Err(); deadlineErr != nil &&
		in.Objective.orFullDefault() == ConvergenceObjectiveParentSafe &&
		!successfulCurrentProof {
		deadlineSummary := formatParentSafetyDeadlineSummary(result.ConvergenceReport)
		result.Converged = false
		result.ParentSafe = false
		result.FinalSnapshot = nil
		result.FinalSnapshotCurrent = false
		result.State = ConvergenceStateNonConverged
		return result, fmt.Errorf("admission parent-safe deadline exceeded: %w; last_report=%s", deadlineErr, deadlineSummary)
	}
	var snapshotErr *SnapshotError
	if errors.As(err, &snapshotErr) && snapshotErr.Class == HierarchyErrorStale {
		result.Converged = false
		result.State = ConvergenceStateNonConverged
		result.FinalSnapshot = nil
		result.FinalSnapshotCurrent = false
	}
	if err != nil &&
		result.ReplanDisposition == ReplanNotAllowed &&
		!result.Published &&
		!result.publicationAttempted &&
		result.Applied == 0 &&
		len(result.Journal) == 0 &&
		!result.hasPhysicalWriteEvidence() &&
		(replanRequired(err) || (snapshotErr != nil && snapshotErr.Class == HierarchyErrorStale)) {
		result.ReplanDisposition = ReplanSafeNoPhysicalWrites
	}
	return result, err
}

func formatParentSafetyDeadlineSummary(report ConvergenceReport) string {
	parts := []string{
		fmt.Sprintf("fully_converged=%v", report.FullyConverged),
		fmt.Sprintf("pending_to_primary=%s", report.PendingToPrimary.String()),
		fmt.Sprintf("pending_to_reclaim=%s", report.PendingToReclaim.String()),
		fmt.Sprintf("cleanup_pending_primary=%s", report.CleanupPendingPrimary.String()),
		fmt.Sprintf("cleanup_pending_reclaim=%s", report.CleanupPendingReclaim.String()),
		fmt.Sprintf("non_converged_count=%d", len(report.NonConvergedTargets)),
	}
	if len(report.NonConvergedTargets) > 0 {
		limit := len(report.NonConvergedTargets)
		if limit > 5 {
			limit = 5
		}
		for i := 0; i < limit; i++ {
			item := report.NonConvergedTargets[i]
			parts = append(parts, fmt.Sprintf("non_converged[%d]=rel:%s reason:%s observed:%s target:%s observed_mems:%s target_mems:%s",
				i, item.Rel, item.Reason, item.Observed.String(), item.Target.String(), item.ObservedMems, item.TargetMems))
		}
	}
	return strings.Join(parts, ",")
}

func (c TopologyCoordinator) convergeNormal(ctx context.Context, in CoordinatorInput, res *ConvergenceResult, budget *BudgetTracker) (ConvergenceResult, error) {
	if len(in.CPUDetails) == 0 {
		return *res, errors.New("TopologyCoordinator.Converge: empty CPUDetails in normal mode")
	}
	pendingRequiredByRel, err := pendingRequiredCPUSetByRel(in.DAG, in.PendingProtections)
	if err != nil {
		return *res, err
	}
	protectedPending := pendingProtectionUnion(in.PendingProtections)
	allowEmptyTarget := in.Cgroup.Version(ctx) == cgroupclient.CgroupVersionV2
	effectiveTargets, err := computeEffectiveTargets(in.DAG, allowEmptyTarget, in.CPUDetails, pendingRequiredByRel, in.RequiredCPUSetByRel, in.ProtectedCPUSetByRel)
	if err != nil {
		return *res, err
	}
	snapshotDriver, err := snapshotDriverForCoordinator(ctx, in.Cgroup)
	if err != nil {
		return *res, err
	}
	defer snapshotDriver.Close()

	round, initialSnapshot, err := c.buildNormalRoundState(ctx, in, budget, snapshotDriver, pendingRequiredByRel, protectedPending, allowEmptyTarget, effectiveTargets)
	if err != nil {
		return *res, err
	}
	if round.objective == ConvergenceObjectiveParentSafe {
		if _, err := round.executeParentSafeAdmission(
			ctx,
			initialSnapshot,
			res,
			in.PublishFinalSnapshot,
			in.PublishParentSafeSnapshot,
		); err != nil {
			return *res, err
		}
		return *res, nil
	}
	round.pendingSnapshot = initialSnapshot

	var lastNoProgressSignature string
	var repeatedNoProgress int
	for {
		outcome, snapshot, err := c.runOneFixedPointRound(ctx, in, res, round)
		if err != nil {
			return *res, err
		}
		evaluation, err := evaluateCoordinatorSnapshot(
			snapshot, in.DAG, round.targetByRel, round.semanticTargetByRel,
			round.desiredMemsByRel(),
			round.desiredDomainUnion(), round.allowedCPUs(),
			in.ExpectedCPUSetByRel, in.RequiredCPUSetByRel, in.DeferredCPUSetByRel,
			round.deferredCleanupRels,
			round.admissionSafetyCPUSet(), round.pendingRequiredByRel,
			snapshotDriver.Capabilities(), allowEmptyTarget,
		)
		if err != nil {
			return *res, err
		}
		res.ConvergenceReport = evaluation.Report
		parentSafeDeferred := round.objective == ConvergenceObjectiveParentSafe &&
			evaluation.ParentSafety.Safe && !evaluation.Report.FullyConverged
		if evaluation.Report.FullyConverged || parentSafeDeferred {
			fresh, freshParentSafeDeferred, err := c.verifyConvergedFreshSnapshot(
				ctx, in, res, round, snapshot, &outcome, snapshotDriver, allowEmptyTarget,
			)
			if err != nil {
				return *res, err
			}
			if err := c.finalizeConvergedRound(ctx, in, res, round, fresh, &outcome, freshParentSafeDeferred); err != nil {
				return *res, err
			}
			return *res, nil
		}
		res.Converged = false
		if outcome.Status == RoundStatusBlocked {
			var structural *deadlock.StructuralV1NonEmptyDeadlock
			if errors.As(outcome.Blocker, &structural) {
				res.State = ConvergenceStateBlocked
				return *res, nil
			}
			signature := noWriteBlockedSignature(snapshot, outcome.Witnesses, evaluation.Report)
			if signature == lastNoProgressSignature {
				repeatedNoProgress++
			} else {
				lastNoProgressSignature = signature
				repeatedNoProgress = 1
			}
			if repeatedNoProgress >= 2 {
				res.State = ConvergenceStateBlocked
				return *res, nil
			}
			res.State = ConvergenceStateNonConverged
			continue
		}
		lastNoProgressSignature = ""
		repeatedNoProgress = 0
		res.State = ConvergenceStateNonConverged
	}
}

// buildNormalRoundState constructs the coordinatorRound and the initial planning
// snapshot used by normal-mode convergence. It owns the round configuration, the
// snapshot source selection, the max-round budget derivation and the auto
// cumulative budget configuration. The caller retains ownership of
// snapshotDriver and must Close it.
func (c TopologyCoordinator) buildNormalRoundState(
	ctx context.Context,
	in CoordinatorInput,
	budget *BudgetTracker,
	snapshotDriver HierarchyDriver,
	pendingRequiredByRel map[string]machine.CPUSet,
	protectedPending machine.CPUSet,
	allowEmptyTarget bool,
	effectiveTargets map[string]machine.CPUSet,
) (*coordinatorRound, *CompleteSnapshot, error) {
	round := newCoordinatorRoundWithBudget(in.DAG, in.Cgroup, in.CPUDetails, in.ReservedCPUSet, in.DrainSelection, budget)
	round.adjustmentBudget = in.AdjustmentBudget
	round.semanticTargetByRel = model.CloneCPUSetMap(effectiveTargets)
	round.dynamicByRel = model.CloneCPUSetMap(in.ExpectedCPUSetByRel)
	round.requiredByRel = model.CloneCPUSetMap(in.RequiredCPUSetByRel)
	round.objective = in.Objective.orFullDefault()
	round.deferredByRel = model.CloneCPUSetMap(in.DeferredCPUSetByRel)
	round.admissionBudget = in.AdmissionBudget
	round.allowEmptyTarget = allowEmptyTarget
	round.protectedPending = protectedPending
	round.pendingRequiredByRel = model.CloneCPUSetMap(pendingRequiredByRel)
	round.protectedByRel = model.CloneCPUSetMap(in.ProtectedCPUSetByRel)
	round.requiredIdentityByRel = cloneIdentityMap(in.RequiredIdentityByRel)
	round.expectedAbsentRels = cloneRelSet(in.ExpectedAbsentRels)
	round.snapshotSource = newCompleteSnapshotSource(snapshotDriver, in.DAG, budget, in.TraversalBoundaries)
	if !allowEmptyTarget {
		round.snapshotSource = newDormantCompleteSnapshotSource(snapshotDriver, in.DAG, budget, in.TraversalBoundaries)
	}
	round.driver = snapshotDriver

	var (
		initialSnapshot *CompleteSnapshot
		err             error
	)
	if in.InitialSnapshot != nil {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		initialSnapshot, err = round.preparePlanningSnapshot(
			ctx, CloneCompleteSnapshot(in.InitialSnapshot))
	} else {
		initialSnapshot, err = round.nextPlanningSnapshot(ctx)
	}
	if err != nil {
		return nil, nil, err
	}
	round.maxRounds = coordinatorMaxRoundsForPlanInput(PhasePlanInput{
		Kind: PhaseDrain, DAG: in.DAG, Snapshot: initialSnapshot,
		DesiredByRel: round.targetByRel, SemanticByRel: round.semanticTargetByRel,
		DormantRels: round.dormantRels, AllowedCPUs: round.allowedCPUs(),
		ProtectedPending: protectedPending, ProtectedByRel: in.ProtectedCPUSetByRel,
		CPUDetails: in.CPUDetails, Selection: round.selection,
	}, in.Budget.MaxRounds)
	autoBudgetInput, err := coordinatorAutoCumulativeBudgetInput(round.maxRounds, in.DAG, initialSnapshot, budget.Usage())
	if err != nil {
		return nil, nil, err
	}
	if err := budget.configureAutoCumulativeLimitsFromInput(autoBudgetInput); err != nil {
		return nil, nil, err
	}
	return round, initialSnapshot, nil
}

// runOneFixedPointRound executes a single engine iteration, records the outcome
// on res.Rounds, and returns the outcome together with the resulting snapshot.
// A non-nil return error means the caller must immediately return *res, err;
// res is already mutated exactly as the original inline loop required.
func (c TopologyCoordinator) runOneFixedPointRound(
	ctx context.Context,
	in CoordinatorInput,
	res *ConvergenceResult,
	round *coordinatorRound,
) (RoundOutcome, *CompleteSnapshot, error) {
	engineResult, engineErr := round.runFixedPointEngine(
		ctx,
		newLivePhaseSession(round, res),
		fixedPointEngineSingleRound,
	)
	outcome := RoundOutcome{}
	if engineResult != nil {
		outcome = engineResult.Outcome
	}
	err := engineErr
	if err != nil {
		err = prioritizeRoundStalePlanError(outcome, err)
		if preflightObservationStale(err) {
			res.Rounds = append(res.Rounds, outcome)
			res.State = ConvergenceStateNonConverged
			return outcome, nil, err
		}
		if replanRequired(err) {
			res.Rounds = append(res.Rounds, outcome)
			authorizeOwnerReplan(res, outcome.Snapshot, in.AdjustmentBudget)
			return outcome, nil, err
		}
		return outcome, nil, err
	}
	res.Rounds = append(res.Rounds, outcome)
	if outcome.Snapshot == nil {
		return outcome, nil, errors.New("TopologyCoordinator.Converge: fixed-point round completed without final snapshot")
	}
	return outcome, outcome.Snapshot, nil
}

// verifyConvergedFreshSnapshot re-reads a fresh snapshot after a converged round
// and proves the convergence objective still holds. On success it returns the
// fresh snapshot and the effective parent-safe-deferred flag; it has already
// refreshed res.ConvergenceReport. On failure it patches the last recorded
// outcome and authorizes replan, returning a non-nil error the caller must
// propagate.
func (c TopologyCoordinator) verifyConvergedFreshSnapshot(
	ctx context.Context,
	in CoordinatorInput,
	res *ConvergenceResult,
	round *coordinatorRound,
	snapshot *CompleteSnapshot,
	outcome *RoundOutcome,
	driver HierarchyDriver,
	allowEmptyTarget bool,
) (*CompleteSnapshot, bool, error) {
	fresh, err := round.nextSnapshot(ctx)
	if err != nil {
		return nil, false, err
	}
	publishExpected := mergeCPUSetMaps(in.ExpectedCPUSetByRel, in.DeferredCPUSetByRel)
	if !publishRelevantSnapshotsEqual(in.DAG, publishExpected, snapshot, fresh) {
		staleErr := &PlanStaleError{
			Rel: "controlled", Direction: WritePublish, Resource: "final_snapshot",
			Current: snapshotLogicalState(fresh), Target: snapshotLogicalState(snapshot),
			Err: fmt.Errorf("fresh publish-relevant snapshot state differs from convergence snapshot"),
		}
		outcome.Status = RoundStatusStale
		outcome.Snapshot = fresh
		outcome.Blocker = staleErr
		res.Rounds[len(res.Rounds)-1] = *outcome
		authorizeOwnerReplan(res, fresh, in.AdjustmentBudget)
		return fresh, false, staleErr
	}
	freshEvaluation, err := evaluateCoordinatorSnapshot(
		fresh, in.DAG, round.targetByRel, round.semanticTargetByRel,
		round.desiredMemsByRel(),
		round.desiredDomainUnion(), round.allowedCPUs(),
		in.ExpectedCPUSetByRel, in.RequiredCPUSetByRel, in.DeferredCPUSetByRel,
		round.deferredCleanupRels,
		round.admissionSafetyCPUSet(), round.pendingRequiredByRel,
		driver.Capabilities(), allowEmptyTarget,
	)
	if err != nil {
		return fresh, false, err
	}
	res.ConvergenceReport = freshEvaluation.Report
	parentSafeDeferred := round.objective == ConvergenceObjectiveParentSafe &&
		freshEvaluation.ParentSafety.Safe && !freshEvaluation.Report.FullyConverged
	if !freshEvaluation.Report.FullyConverged && !parentSafeDeferred {
		staleErr := &PlanStaleError{
			Rel: "controlled", Direction: WritePublish, Resource: "fresh_convergence_proof",
			Current: snapshotLogicalState(fresh), Target: snapshotLogicalState(snapshot),
			Err: fmt.Errorf("fresh snapshot no longer satisfies the convergence objective"),
		}
		outcome.Status = RoundStatusStale
		outcome.Snapshot = fresh
		outcome.Blocker = staleErr
		res.Rounds[len(res.Rounds)-1] = *outcome
		authorizeOwnerReplan(res, fresh, in.AdjustmentBudget)
		return fresh, false, staleErr
	}
	return fresh, parentSafeDeferred, nil
}

// finalizeConvergedRound records the terminal converged state on res and runs
// the final snapshot publication. It returns nil when publication succeeded
// (caller then returns *res, nil); any non-nil error has already been reflected
// on res and must be propagated by the caller.
func (c TopologyCoordinator) finalizeConvergedRound(
	ctx context.Context,
	in CoordinatorInput,
	res *ConvergenceResult,
	round *coordinatorRound,
	fresh *CompleteSnapshot,
	outcome *RoundOutcome,
	parentSafeDeferred bool,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	res.Converged = true
	res.State = ConvergenceStateConverged
	if parentSafeDeferred {
		res.Converged = false
		res.ParentSafe = true
		res.State = ConvergenceStateParentSafeLeafDeferred
		res.DeferredLeafCount = len(in.DeferredCPUSetByRel)
		for _, cpus := range in.DeferredCPUSetByRel {
			res.DeferredCPUCount += cpus.Size()
		}
	}
	outcome.Status = RoundStatusConverged
	outcome.Snapshot = fresh
	res.Rounds[len(res.Rounds)-1] = *outcome
	res.FinalSnapshot = CloneCompleteSnapshot(fresh)
	res.FinalSnapshotCurrent = true
	publish := func() error {
		if parentSafeDeferred && in.PublishParentSafeSnapshot != nil {
			deferredCleanupRels := make(map[string]struct{}, len(round.deferredCleanupRels))
			for rel := range round.deferredCleanupRels {
				deferredCleanupRels[rel] = struct{}{}
			}
			return in.PublishParentSafeSnapshot(CloneCompleteSnapshot(fresh), deferredCleanupRels)
		}
		if in.PublishFinalSnapshot != nil {
			return in.PublishFinalSnapshot(CloneCompleteSnapshot(fresh))
		}
		return nil
	}
	res.publicationAttempted = parentSafeDeferred && in.PublishParentSafeSnapshot != nil ||
		!parentSafeDeferred && in.PublishFinalSnapshot != nil
	if err := publish(); err != nil {
		res.Published = false
		if errors.Is(err, ErrCoordinatorPlanStale) {
			res.ReplanDisposition = ReplanSafeFromVerifiedFinalState
			return err
		}
		res.ReplanDisposition = ReplanNotAllowed
		res.FinalSnapshotCurrent = false
		res.FinalSnapshot = nil
		res.Converged = false
		res.ParentSafe = false
		return err
	}
	res.Published = true
	res.ReplanDisposition = ReplanNotAllowed
	return nil
}

// authorizeOwnerReplan records only evidence that can cross the coordinator
// boundary. A stale compile with no attempted mutation is safe to restart from
// a new snapshot. Once any write was attempted, only a complete snapshot taken
// after that attempt may authorize the plugin to compile again.
func authorizeOwnerReplan(
	res *ConvergenceResult,
	snapshot *CompleteSnapshot,
	budget *AdjustmentBudget,
) {
	if res == nil {
		return
	}
	res.Converged = false
	res.ParentSafe = false
	res.State = ConvergenceStateNonConverged
	res.Published = false
	if !res.hasPhysicalWriteEvidence() {
		res.FinalSnapshot = nil
		res.FinalSnapshotCurrent = false
		res.ReplanDisposition = ReplanSafeNoPhysicalWrites
		return
	}
	if snapshot == nil || !budget.ReplanSafe(ReplanSafeFromVerifiedFinalState) {
		res.FinalSnapshot = nil
		res.FinalSnapshotCurrent = false
		res.ReplanDisposition = ReplanNotAllowed
		return
	}
	res.FinalSnapshot = CloneCompleteSnapshot(snapshot)
	res.FinalSnapshotCurrent = true
	res.ReplanDisposition = ReplanSafeFromVerifiedFinalState
}

func coordinatorAutoCumulativeBudgetInput(
	rounds int,
	dag *TopoDAG,
	snapshot *CompleteSnapshot,
	usage BudgetUsage,
) (AutoCumulativeBudgetInput, error) {
	if dag == nil || snapshot == nil {
		return AutoCumulativeBudgetInput{}, fmt.Errorf("%w: DAG and initial snapshot are required", ErrAutoCumulativeBudgetInvalid)
	}
	snapshotNodes := len(snapshot.Entries)
	if snapshotNodes == 0 {
		snapshotNodes = 1
	}
	snapshotIO := snapshot.Cost.HierarchyIOOperations
	if snapshotIO <= 0 {
		snapshotIO = saturatingAdd(saturatingMultiply(snapshotNodes, 2), 1)
	}
	drainFrontiers, err := checkedAutoBudgetMultiply(rounds, len(dag.index))
	if err != nil {
		return AutoCumulativeBudgetInput{}, err
	}
	growDomains, err := checkedAutoBudgetMultiply(rounds, len(snapshot.DomainUnion))
	if err != nil {
		return AutoCumulativeBudgetInput{}, err
	}
	planOperations, err := coordinatorAutoPlanOperationsTotal(rounds, snapshot)
	if err != nil {
		return AutoCumulativeBudgetInput{}, err
	}
	childMemberships, err := checkedAutoBudgetMultiply(rounds, snapshotChildEdgeCount(snapshot))
	if err != nil {
		return AutoCumulativeBudgetInput{}, err
	}
	return AutoCumulativeBudgetInput{
		CurrentUsedIO:            usage.HierarchyIOOperations,
		RemainingRounds:          rounds,
		SnapshotIOUpperBound:     snapshotIO,
		MaxDrainFrontiersTotal:   drainFrontiers,
		MaxGrowDomainsTotal:      growDomains,
		MaxPlanOperationsTotal:   planOperations,
		MaxChildMembershipsTotal: childMemberships,
		StaleRetryAllowance:      rounds,
	}, nil
}

func coordinatorAutoPlanOperationsTotal(rounds int, snapshot *CompleteSnapshot) (int, error) {
	if rounds <= 0 || snapshot == nil {
		return 0, fmt.Errorf("%w: positive rounds and initial snapshot are required for plan operations",
			ErrAutoCumulativeBudgetInvalid)
	}
	snapshotNodes := len(snapshot.Entries)
	if snapshotNodes == 0 {
		snapshotNodes = 1
	}

	// One drain build charges a projection walk over every entry and child
	// membership, followed by at most one monotonic shrink operation per entry.
	// Expand likewise contributes at most one monotonic grow operation per entry.
	initialPlan, err := checkedAutoBudgetSum(
		snapshotNodes,
		snapshotChildEdgeCount(snapshot),
		snapshotNodes,
	)
	if err != nil {
		return 0, err
	}
	perRound, err := checkedAutoBudgetSum(initialPlan, snapshotNodes)
	if err != nil {
		return 0, err
	}

	// Drain executes one depth frontier at a time. Every completed frontier
	// rebases the plan and charges the number of operations still pending.
	// Derive that cumulative triangular term from the initial hierarchy shape:
	// wide siblings share one frontier, while a deep chain has one per depth.
	depthByRel, _ := buildSnapshotDepthByRel(snapshot, nil)
	frontierWidths := make(map[int]int)
	for rel := range snapshot.Entries {
		frontierWidths[depthByRel[rel]]++
	}
	depths := make([]int, 0, len(frontierWidths))
	for depth := range frontierWidths {
		depths = append(depths, depth)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(depths)))
	remaining := len(snapshot.Entries)
	for _, depth := range depths {
		remaining -= frontierWidths[depth]
		perRound, err = checkedAutoBudgetSum(perRound, remaining)
		if err != nil {
			return 0, err
		}
	}
	return checkedAutoBudgetMultiply(rounds, perRound)
}

func prioritizeRoundStalePlanError(outcome RoundOutcome, err error) error {
	if err == nil || outcome.Blocker == nil || !replanRequired(outcome.Blocker) {
		return err
	}
	if errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		return outcome.Blocker
	}
	return err
}

func includeMaterializedDynamicConvergence(
	report *ConvergenceReport,
	snapshot *CompleteSnapshot,
	expected map[string]machine.CPUSet,
	capabilities HierarchyCapabilities,
) {
	if report == nil || snapshot == nil {
		return
	}
	for rel, target := range expected {
		entry, materialized := snapshot.Entries[rel]
		if !materialized || observedCPUsForTargetProof(entry, target, capabilities).Equals(target) {
			continue
		}
		report.NonConvergedTargets = append(report.NonConvergedTargets, RelConvergence{
			Rel: rel, Observed: observedCPUsForTargetProof(entry, target, capabilities).Clone(), Target: target.Clone(),
			ObservedMems: entry.Mems, Reason: "dynamic_target_mismatch",
		})
	}
	report.FullyConverged = report.FullyConverged && len(report.NonConvergedTargets) == 0
}

func publishRelevantSnapshotsEqual(
	dag *TopoDAG,
	expectedCPUSetByRel map[string]machine.CPUSet,
	converged, fresh *CompleteSnapshot,
) bool {
	if dag == nil || converged == nil || fresh == nil {
		return false
	}
	for rel := range dag.index {
		if !publishRelevantEntriesEqual(converged.Entries, fresh.Entries, rel) {
			return false
		}
	}
	for rel := range expectedCPUSetByRel {
		_, wasMaterialized := converged.Entries[rel]
		_, isMaterialized := fresh.Entries[rel]
		if !wasMaterialized && !isMaterialized {
			continue
		}
		if !publishRelevantEntriesEqual(converged.Entries, fresh.Entries, rel) {
			return false
		}
	}
	return true
}

func publishRelevantEntriesEqual(before, after map[string]EntryState, rel string) bool {
	beforeEntry, beforeExists := before[rel]
	afterEntry, afterExists := after[rel]
	return beforeExists == afterExists &&
		beforeExists &&
		beforeEntry.Identity == afterEntry.Identity &&
		beforeEntry.CPUs.Equals(afterEntry.CPUs) &&
		beforeEntry.Mems == afterEntry.Mems &&
		beforeEntry.ConfiguredCPUs.Equals(afterEntry.ConfiguredCPUs) &&
		beforeEntry.ConfiguredMems == afterEntry.ConfiguredMems &&
		beforeEntry.Activity == afterEntry.Activity
}

func roundOutcomeMadeNetProgress(outcome RoundOutcome) bool {
	measure := outcome.Progress
	if measure.DrainChangedRels == 0 {
		measure.DrainChangedRels = len(outcome.ChangedRels)
	}
	for _, applied := range outcome.Journal {
		if applied.PhysicalImpact == PhysicalImpactUncertain {
			continue
		}
		if applied.Observed.CPUs.Equals(applied.Target.CPUs) && applied.Observed.Mems == applied.Target.Mems {
			measure.VerifiedWrites++
		}
	}
	return measure.MadeProgress()
}

func snapshotRelGeneration(snapshot *CompleteSnapshot, rel string) string {
	if snapshot == nil {
		return "unknown"
	}
	entry, ok := snapshot.Entries[rel]
	if !ok {
		return "missing"
	}
	return fmt.Sprintf("%v", entry.Identity)
}

func snapshotLogicalState(snapshot *CompleteSnapshot) string {
	if snapshot == nil {
		return "unknown"
	}
	rels := make([]string, 0, len(snapshot.Entries))
	for rel := range snapshot.Entries {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	var b strings.Builder
	for _, rel := range rels {
		entry := snapshot.Entries[rel]
		_, _ = fmt.Fprintf(&b, "%s:%v:%s:%s:%s:%s;",
			rel, entry.Identity, entry.CPUs.String(), entry.Mems,
			entry.ConfiguredCPUs.String(), entry.ConfiguredMems)
	}
	return b.String()
}

func noWriteBlockedSignature(snapshot *CompleteSnapshot, witnesses []ReleaseWitness, report ConvergenceReport) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "state=%s;", snapshotLogicalState(snapshot))
	witnessParts := make([]string, 0, len(witnesses))
	for _, witness := range witnesses {
		witnessParts = append(witnessParts, fmt.Sprintf("%s>%s:%s", witness.Source, witness.Destination, witness.CPUs.String()))
	}
	sort.Strings(witnessParts)
	b.WriteString("witnesses=")
	for _, part := range witnessParts {
		_, _ = fmt.Fprintf(&b, "%s,", part)
	}
	mismatchParts := make([]string, 0, len(report.NonConvergedTargets))
	for _, mismatch := range report.NonConvergedTargets {
		mismatchParts = append(mismatchParts, fmt.Sprintf("%s:%s:%s:%s:%s",
			mismatch.Rel, snapshotRelGeneration(snapshot, mismatch.Rel),
			mismatch.Observed.String(), mismatch.Target.String(), mismatch.Reason))
	}
	sort.Strings(mismatchParts)
	b.WriteString(";mismatch=")
	for _, part := range mismatchParts {
		_, _ = fmt.Fprintf(&b, "%s,", part)
	}
	_, _ = fmt.Fprintf(&b, ";pending=%s/%s/%s/%s",
		report.PendingToPrimary.String(), report.PendingToReclaim.String(),
		report.CleanupPendingPrimary.String(), report.CleanupPendingReclaim.String())
	return b.String()
}

func replanRequired(err error) bool {
	var stale interface{ ReplanRequired() bool }
	return (errors.As(err, &stale) && stale.ReplanRequired()) || errors.Is(err, ErrCgroupIdentityChanged)
}

func preflightObservationStale(err error) bool {
	var stale *PlanStaleError
	return errors.As(err, &stale) && strings.HasPrefix(stale.Resource, "preflight_")
}

func (c TopologyCoordinator) convergeReset(ctx context.Context, in CoordinatorInput, res *ConvergenceResult, budget *BudgetTracker) (ConvergenceResult, error) {
	allowEmptyTarget := in.Cgroup.Version(ctx) == cgroupclient.CgroupVersionV2
	targets := desiredTargets(in.DAG)
	driver, err := snapshotDriverForCoordinator(ctx, in.Cgroup)
	if err != nil {
		return *res, err
	}
	defer driver.Close()
	if in.Budget.MaxHierarchyIOOperations == 0 {
		initialSnapshot, err := newCompleteSnapshotSource(driver, in.DAG, budget, in.TraversalBoundaries)(ctx)
		if err != nil {
			return *res, err
		}
		autoBudgetInput, err := coordinatorAutoCumulativeBudgetInput(1, in.DAG, initialSnapshot, budget.Usage())
		if err != nil {
			return *res, err
		}
		if err := budget.configureAutoCumulativeLimitsFromInput(autoBudgetInput); err != nil {
			return *res, err
		}
	}
	writer := newResetCoordinatorWriter(driver, budget, in.Mems, res).
		withAdjustmentBudget(in.AdjustmentBudget)
	if err := writer.execute(ctx, in.DAG, targets, allowEmptyTarget, in.ExpectedCPUSetByRel, in.TraversalBoundaries); err != nil {
		return *res, err
	}
	observationEpoch := in.AdjustmentBudget.BeginSnapshotObservation()
	report, err := verifyResetConvergence(ctx, driver, budget, in.DAG, targets)
	if err != nil {
		return *res, err
	}
	in.AdjustmentBudget.ObserveCurrentSnapshot(observationEpoch)
	res.ConvergenceReport = report
	res.Converged = report.FullyConverged
	if res.Converged {
		res.State = ConvergenceStateConverged
		res.Published = true
	} else {
		res.State = ConvergenceStateNonConverged
	}
	return *res, nil
}

func snapshotDriverForCoordinator(ctx context.Context, cg cgroupclient.CgroupClient) (HierarchyDriver, error) {
	if provider, ok := cg.(interface{ SnapshotDriver() HierarchyDriver }); ok {
		driver := provider.SnapshotDriver()
		if driver == nil {
			return nil, errors.New("TopologyCoordinator.Converge: nil snapshot driver")
		}
		return driver, nil
	}
	return newCoordinatorHierarchyDriver(ctx, cg, cgroupRootPathCPUSet())
}

func newCoordinatorHierarchyDriver(
	ctx context.Context,
	cg cgroupclient.CgroupClient,
	rootPath string,
) (HierarchyDriver, error) {
	// CgroupClient only handles version detection and partition, sched-load-balance,
	// and PID attachment above the coordinator; hierarchy snapshots and writes use the pinned-root FD engine.
	if cg.Version(ctx) == cgroupclient.CgroupVersionV2 {
		driver, err := NewCgroupV2Driver(rootPath, nil)
		if err != nil {
			return nil, fmt.Errorf("TopologyCoordinator.Converge: create cgroup v2 snapshot driver: %w", err)
		}
		return driver, nil
	}
	driver, err := NewCgroupV1Driver(rootPath, nil)
	if err != nil {
		return nil, fmt.Errorf("TopologyCoordinator.Converge: create cgroup v1 snapshot driver: %w", err)
	}
	return driver, nil
}

type coordinatorRound struct {
	dag                    *TopoDAG
	targetByRel            map[string]machine.CPUSet
	semanticTargetByRel    map[string]machine.CPUSet
	dormantRels            map[string]struct{}
	dormantProofs          map[string]DormantLeafProof
	dynamicByRel           map[string]machine.CPUSet
	deferredByRel          map[string]machine.CPUSet
	requiredByRel          map[string]machine.CPUSet
	deferredCleanupRels    map[string]struct{}
	objective              ConvergenceObjective
	admissionBudget        *AdmissionConvergenceBudget
	allowEmptyTarget       bool
	protectedPending       machine.CPUSet
	pendingRequiredByRel   map[string]machine.CPUSet
	protectedByRel         map[string]machine.CPUSet
	requiredIdentityByRel  map[string]CgroupIdentity
	expectedAbsentRels     map[string]struct{}
	cpuDetails             machine.CPUDetails
	reservedCPUs           machine.CPUSet
	selection              DrainSelectionPolicy
	snapshotSource         func(context.Context) (*CompleteSnapshot, error)
	driver                 HierarchyDriver
	budget                 *BudgetTracker
	adjustmentBudget       *AdjustmentBudget
	recoveryContextFactory func(int) (context.Context, context.CancelFunc, error)
	planID                 string
	witnesses              []ReleaseWitness
	blocked                map[DomainID]machine.CPUSet
	pendingSnapshot        *CompleteSnapshot
	round                  int
	maxRounds              int
}

func aggregateExecutionErrors(primary, secondary error) error {
	return apierrors.NewAggregate([]error{primary, secondary})
}

// executeParentSafeAdmission is the sole owner of the ParentSafe hierarchy
// mutation transaction. It compiles a closed trace from base on an isolated
// projection, reserves that exact immutable trace, preflights all evidence, and
// executes without live replanning. Compilation, reservation, or preflight
// failure performs no writes; later failure is successful only as an atomic
// failure after the written prefix has been rolled back and verified.
func (r *coordinatorRound) executeParentSafeAdmission(
	ctx context.Context,
	base *CompleteSnapshot,
	res *ConvergenceResult,
	publishFinal func(*CompleteSnapshot) error,
	publishParentSafe func(*CompleteSnapshot, map[string]struct{}) error,
) (outcome RoundOutcome, err error) {
	outcome = RoundOutcome{Status: RoundStatusBlocked}
	if r == nil || r.objective.orFullDefault() != ConvergenceObjectiveParentSafe {
		return outcome, fmt.Errorf("frozen admission execution requires ParentSafe objective")
	}
	if res == nil {
		return outcome, fmt.Errorf("frozen admission execution requires convergence result")
	}

	// maxParentSafeChurnAbsorptions bounds in-transaction recompilations that
	// absorb dynamic frozen-boundary holder churn without consuming the caller's
	// replan budget. A single fresh-snapshot retry is enough: holder churn is a
	// transient race between compilation and preflight, and the fresh snapshot
	// already contains the newly appeared holder.
	const maxParentSafeChurnAbsorptions = 1

	baseSnapshot := base
	var (
		adjustmentReservation *AdjustmentWriteReservation
		forwardStart          int
		rollbackStart         int
		churnAbsorbed         int
	)
	defer func() {
		if adjustmentReservation == nil {
			return
		}
		// Settle the final reservation against the actually observed write counts.
		// A mismatch means write accounting disagreed with execution; surface it
		// (Warningf) without clobbering a more important execution error: only
		// replace err when there is no other failure, otherwise append the
		// accounting failure to the error chain.
		if settleErr := adjustmentReservation.settleTotals(
			res.forwardWriteAttempts-forwardStart,
			res.rollbackWriteAttempts-rollbackStart,
		); settleErr != nil {
			general.Warningf("cpuset_topology: adjustment reservation settle mismatch: %v", settleErr)
			switch err {
			case nil:
				err = settleErr
			default:
				err = aggregateExecutionErrors(err, settleErr)
			}
		}
	}()

	var publishStaleErr error
	finalize := func(
		ctx context.Context,
		frozen *CompiledPhaseTrace,
	) (frozenTraceFinalization, error) {
		finalization, finalizeErr := r.proveFrozenTraceFinalState(ctx, frozen)
		if finalizeErr != nil {
			return finalization, finalizeErr
		}
		if finalizeErr = ctx.Err(); finalizeErr != nil {
			return finalization, finalizeErr
		}
		parentSafeDeferred := finalization.evaluation.ParentSafety.Safe &&
			!finalization.evaluation.Report.FullyConverged
		switch {
		case parentSafeDeferred && publishParentSafe != nil:
			res.publicationAttempted = true
			finalizeErr = publishParentSafe(
				CloneCompleteSnapshot(finalization.snapshot),
				cloneRelSet(frozen.EvaluationInput.DeferredCleanupRels),
			)
		case finalization.evaluation.Report.FullyConverged && publishFinal != nil:
			res.publicationAttempted = true
			finalizeErr = publishFinal(CloneCompleteSnapshot(finalization.snapshot))
		}
		if errors.Is(finalizeErr, ErrCoordinatorPlanStale) {
			publishStaleErr = finalizeErr
			finalizeErr = nil
		}
		return finalization, finalizeErr
	}

	for {
		validated, err := r.compileValidatedFixedPointTrace(ctx, baseSnapshot)
		if err != nil {
			return outcome, err
		}
		trace, err := validated.trace()
		if err != nil {
			return outcome, err
		}

		maxRequiredWrites := 0
		if r.admissionBudget != nil {
			maxRequiredWrites = r.admissionBudget.MaxRequiredWrites
		}
		ticket, err := r.budget.reserveValidatedPhaseTrace(ctx, validated, maxRequiredWrites)
		if err != nil {
			return outcome, err
		}
		adjustmentReservation, err = r.adjustmentBudget.ReserveExecution(trace.Cost)
		if err != nil {
			return outcome, err
		}
		forwardStart = res.forwardWriteAttempts
		rollbackStart = res.rollbackWriteAttempts

		outcome, err = r.executeValidatedFrozenTrace(
			ctx, validated, ticket, res, adjustmentReservation, finalize)
		res.Rounds = append(res.Rounds, outcome)
		if err != nil {
			res.Published = false
			// Absorb a transient dynamic frozen-boundary drift in place: the new
			// holder/coverage already exists in the live hierarchy, so a fresh
			// snapshot folds it into the plan without any physical write and
			// without consuming the caller's replan budget.
			if churnAbsorbed < maxParentSafeChurnAbsorptions &&
				!res.hasPhysicalWriteEvidence() &&
				isFrozenBoundaryDynamicDrift(err) {
				churnAbsorbed++
				general.InfofV(4, "cpuset_topology: absorbed frozen boundary dynamic drift, retrying with fresh snapshot, attempt=%d", churnAbsorbed)
				fresh, snapErr := r.nextPlanningSnapshot(ctx)
				if snapErr != nil {
					// Prefer the original stale evidence over a snapshot error:
					// the caller already has a replan-safe classification.
					return outcome, err
				}
				// The deferred settle only runs at function return, so release
				// this iteration's reservation explicitly before recompiling.
				// Zero writes are guaranteed by the hasPhysicalWriteEvidence
				// guard above, so settling (0,0) matches the attempted counts.
				// Fail closed on a mismatch: settleTotals leaves the reservation
				// unsettled and does not release reservedWrites. Continuing would
				// overwrite adjustmentReservation and leak this round's reserved
				// writes permanently (the defer only settles the last round), so
				// surface both the accounting failure and the original stale.
				if settleErr := adjustmentReservation.settleTotals(0, 0); settleErr != nil {
					return outcome, fmt.Errorf("churn absorption reservation settle failed: %w (original stale: %v)", settleErr, err)
				}
				baseSnapshot = fresh
				continue
			}
			return outcome, err
		}
		if publishStaleErr != nil {
			res.Published = false
			res.ReplanDisposition = ReplanSafeFromVerifiedFinalState
			return outcome, publishStaleErr
		}
		res.Published = true
		res.ReplanDisposition = ReplanNotAllowed

		if res.ParentSafe {
			res.DeferredLeafCount = len(trace.EvaluationInput.DeferredByRel)
			for _, cpus := range trace.EvaluationInput.DeferredByRel {
				res.DeferredCPUCount += cpus.Size()
			}
		}
		return outcome, nil
	}
}

// isFrozenBoundaryDynamicDrift reports whether err carries a frozen-boundary
// PlanStaleError on the dynamic (non-controlled) holder set. All such stales
// originate from evaluateFrozenBoundarySnapshot's preflight pass before any
// physical write, so a single bounded fresh-snapshot recompile is safe. This
// covers both new-holder appearance and holder-coverage drift; persistent drift
// on retry surfaces normally to the caller. The preflight wraps the boundary
// stale inside an initial-snapshot-drift envelope, so the whole error chain is
// walked rather than only inspecting the first PlanStaleError.
func isFrozenBoundaryDynamicDrift(err error) bool {
	for current := err; current != nil; {
		var stale *PlanStaleError
		if !errors.As(current, &stale) {
			return false
		}
		if stale.Resource == "frozen_boundary" && stale.Rel == "dynamic" {
			return true
		}
		current = errors.Unwrap(stale)
	}
	return false
}

func newCoordinatorRoundWithBudget(
	dag *TopoDAG,
	cg cgroupclient.CgroupClient,
	cpuDetails machine.CPUDetails,
	reservedCPUs machine.CPUSet,
	selection DrainSelectionPolicy,
	budget *BudgetTracker,
) *coordinatorRound {
	r := &coordinatorRound{
		dag: dag, cpuDetails: cpuDetails,
		reservedCPUs: reservedCPUs.Clone(), budget: budget, maxRounds: budget.limit.MaxRounds,
		selection: NormalizeDrainSelectionPolicy(selection),
		blocked:   make(map[DomainID]machine.CPUSet),
	}
	if provider, ok := cg.(interface{ SnapshotDriver() HierarchyDriver }); ok {
		r.driver = provider.SnapshotDriver()
		r.snapshotSource = newCompleteSnapshotSource(r.driver, dag, budget)
	}
	return r
}

func DefaultDrainSelectionPolicy() DrainSelectionPolicy {
	return DrainSelectionPolicy{
		MaxCPUsDrainRatio:         0,
		GroupByNUMA:               false,
		RequirePairedSwapProgress: true,
	}
}

func NormalizeDrainSelectionPolicy(policy DrainSelectionPolicy) DrainSelectionPolicy {
	defaults := DefaultDrainSelectionPolicy()
	if policy.MaxCPUsDrainRatio == 0 && !policy.GroupByNUMA && !policy.RequirePairedSwapProgress {
		return defaults
	}
	return policy
}

func coordinatorMaxRoundsForPlanInput(in PhasePlanInput, explicitMaxRounds int) int {
	if explicitMaxRounds != 0 {
		return explicitMaxRounds
	}
	required := defaultCoordinatorAutoRounds
	if in.DAG == nil || in.Snapshot == nil {
		return required
	}

	desiredByDomain := desiredDomainUnions(in.DAG, semanticTargetsForPlan(in))
	protectedByDomain := protectedCPUSetByDomain(in.ProtectedByRel, in.ProtectedPending, in.DAG)

	batchLimit := maxCPUsPerDrainRound(len(in.CPUDetails), in.Selection.MaxCPUsDrainRatio)
	for _, domain := range sortedDomains(in.Snapshot.DomainUnion, desiredByDomain) {
		// Count every CPU that must leave this domain, not only CPUs paired to
		// another domain's deficit. The latter misses pure cleanup work.
		workload := in.Snapshot.DomainUnion[domain].
			Difference(desiredByDomain[domain]).
			Difference(protectedByDomain[domain])
		transferRounds := 1
		if batchLimit > 0 {
			transferRounds = (workload.Size() + batchLimit - 1) / batchLimit
		}
		if workload.IsEmpty() {
			transferRounds = 0
		}
		candidate := transferRounds + coordinatorPhaseCompletionRounds
		if candidate > required {
			required = candidate
		}
	}
	return required
}

func (r *coordinatorRound) allowedCPUs() machine.CPUSet {
	if len(r.cpuDetails) == 0 {
		return machine.NewCPUSet()
	}
	return r.cpuDetails.CPUs().Difference(r.reservedCPUs)
}

func (r *coordinatorRound) desiredDomainUnion() map[DomainID]machine.CPUSet {
	targets := r.semanticTargetByRel
	if targets == nil {
		targets = r.targetByRel
	}
	return desiredDomainUnions(r.dag, targets)
}

func (r *coordinatorRound) desiredMemsByRel() map[string]string {
	out := make(map[string]string, len(r.dag.index))
	for rel, node := range r.dag.index {
		out[rel] = node.Mems
	}
	return out
}

func (r *coordinatorRound) nextRawSnapshot(ctx context.Context) (*CompleteSnapshot, error) {
	if r.pendingSnapshot != nil {
		snapshot := r.pendingSnapshot
		r.pendingSnapshot = nil
		return snapshotWithoutOwnershipOverlay(snapshot), nil
	}
	if r.snapshotSource == nil {
		return nil, fmt.Errorf("topology coordinator requires complete snapshot source")
	}
	if r.budget == nil {
		return nil, fmt.Errorf("topology coordinator requires convergence budget")
	}
	observationEpoch := r.adjustmentBudget.BeginSnapshotObservation()
	snapshot, err := r.snapshotSource(ctx)
	if err != nil {
		return nil, err
	}
	r.adjustmentBudget.ObserveCurrentSnapshot(observationEpoch)
	return snapshot, nil
}

// nextPlanningSnapshot starts each fixed-point round from current physical
// evidence and derives a new immutable semantic/physical materialization.
// Reclassification is intentionally limited to round boundaries; snapshots
// taken while executing a plan must continue to validate the plan's proof.
func (r *coordinatorRound) nextPlanningSnapshot(ctx context.Context) (*CompleteSnapshot, error) {
	snapshot, err := r.nextRawSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return r.preparePlanningSnapshot(ctx, snapshot)
}

func (r *coordinatorRound) preparePlanningSnapshot(
	ctx context.Context,
	snapshot *CompleteSnapshot,
) (*CompleteSnapshot, error) {
	if snapshot == nil {
		return nil, errors.New("topology planning snapshot is nil")
	}
	materialized := materializeTargets(
		r.dag, snapshot, r.allowEmptyTarget, r.semanticTargetByRel)
	r.targetByRel = materialized.PhysicalByRel
	r.semanticTargetByRel = materialized.SemanticByRel
	r.dormantRels = materialized.DormantRels
	r.dormantProofs = materialized.DormantProofs
	return materialized.Snapshot, r.validatePreflightObservations(ctx, materialized.Snapshot)
}

func (r *coordinatorRound) nextSnapshot(ctx context.Context) (*CompleteSnapshot, error) {
	snapshot, err := r.nextRawSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return r.prepareSnapshot(ctx, snapshot)
}

func (r *coordinatorRound) prepareSnapshot(ctx context.Context, snapshot *CompleteSnapshot) (*CompleteSnapshot, error) {
	if err := validateDormantProofs(snapshot, r.dormantProofs); err != nil {
		return snapshot, err
	}
	snapshot = snapshotWithDormantOwnershipOverlay(snapshot, r.dormantRels, r.semanticTargetByRel)
	return snapshot, r.validatePreflightObservations(ctx, snapshot)
}

func (r *coordinatorRound) validatePreflightObservations(ctx context.Context, snapshot *CompleteSnapshot) error {
	for rel, required := range r.requiredIdentityByRel {
		entry, ok := snapshot.Entries[rel]
		if !ok || entry.Identity != required {
			current := "missing"
			if ok {
				current = fmt.Sprintf("%v", entry.Identity)
			}
			return &PlanStaleError{
				Rel:       rel,
				Direction: WritePublish,
				Resource:  "preflight_identity",
				Current:   current,
				Target:    fmt.Sprintf("%v", required),
			}
		}
	}
	driver := r.driver
	if wrapped, ok := driver.(*budgetedHierarchyDriver); !ok || wrapped.budget != r.budget {
		driver = NewBudgetedHierarchyDriver(driver, r.budget)
	}
	for rel := range r.expectedAbsentRels {
		identity, err := driver.StatIdentity(ctx, rel)
		if err == nil {
			return &PlanStaleError{
				Rel:       rel,
				Direction: WritePublish,
				Resource:  "preflight_absence",
				Current:   fmt.Sprintf("%v", identity),
				Target:    "absent",
			}
		}
		if !isCgroupNotFoundError(err) {
			return fmt.Errorf("validate expected absent rel %q: %w", rel, err)
		}
	}
	return nil
}

func cloneIdentityMap(in map[string]CgroupIdentity) map[string]CgroupIdentity {
	if in == nil {
		return nil
	}
	out := make(map[string]CgroupIdentity, len(in))
	for rel, identity := range in {
		out[rel] = identity
	}
	return out
}

func cloneRelSet(in map[string]struct{}) map[string]struct{} {
	if in == nil {
		return nil
	}
	out := make(map[string]struct{}, len(in))
	for rel := range in {
		out[rel] = struct{}{}
	}
	return out
}

func (r *coordinatorRound) buildPlan(ctx context.Context, kind PhaseKind, snapshot *CompleteSnapshot) (PhasePlan, error) {
	plan, err := BuildPhasePlan(PhasePlanInput{
		Context: ctx, Kind: kind, DAG: r.dag, Snapshot: snapshot,
		DesiredByRel: r.targetByRel, SemanticByRel: r.semanticTargetByRel,
		DynamicByRel: r.dynamicByRel, DesiredMemsByRel: r.desiredMemsByRel(),
		DormantRels: r.dormantRels,
		AllowedCPUs: r.allowedCPUs(), AllowEmptyTarget: r.allowEmptyTarget,
		Capabilities: snapshot.Capabilities,
		Witnesses:    r.witnesses, ProtectedPending: r.protectedPending,
		ProtectedByRel: r.protectedByRel, CPUDetails: r.cpuDetails,
		Selection: r.selection, Budget: r.budget,
	})
	if err == nil && r.objective == ConvergenceObjectiveParentSafe {
		required, deferred, splitErr := SplitPlanForAdmission(&plan, AdmissionSafetyInput{
			PendingCPUSet:        r.admissionSafetyCPUSet(),
			PendingRequiredByRel: r.pendingRequiredByRel,
			DeferredCPUSetByRel:  r.deferredByRel,
			RequiredCPUSetByRel:  r.requiredByRel,
		})
		if splitErr != nil {
			return PhasePlan{}, splitErr
		}
		for _, operation := range deferred.Operations {
			r.deferredCleanupRels[operation.Rel] = struct{}{}
		}
		plan = *required
	}
	if err == nil {
		r.planID = plan.PlanID
	}
	return plan, err
}

func (r *coordinatorRound) admissionSafetyCPUSet() machine.CPUSet {
	return r.protectedPending.Clone()
}

func (r *coordinatorRound) executePlan(ctx context.Context, plan PhasePlan, res *ConvergenceResult) error {
	if plan.PlanID == "" || plan.PlanID != r.planID || canonicalExecutionPlanID(plan) != plan.PlanID {
		return fmt.Errorf("phase writer requires current canonical PlanID")
	}
	if r.driver == nil {
		return fmt.Errorf("phase writer requires hierarchy driver")
	}
	if r.budget == nil {
		return fmt.Errorf("phase writer requires convergence budget")
	}
	if err := r.revalidateGrowAuthorization(ctx, plan); err != nil {
		return err
	}
	writer := newSafeCPUSetWriter(r.driver, r.budget, res).
		withAdjustmentBudget(r.adjustmentBudget)
	writer.dormantProofs = r.dormantProofs
	return writer.execute(ctx, plan)
}

func (r *coordinatorRound) revalidateGrowAuthorization(ctx context.Context, plan PhasePlan) error {
	operationsByDomain := make(map[DomainID][]PlanOperation)
	for _, operation := range plan.Operations {
		if operation.Direction != WriteGrow {
			continue
		}
		domain := plan.Base.DomainByRel[operation.Rel]
		if domain == "" {
			if node := r.dag.index[operation.Rel]; node != nil {
				domain = node.Domain
			}
		}
		operationsByDomain[domain] = append(operationsByDomain[domain], operation)
	}
	if len(operationsByDomain) == 0 {
		return nil
	}
	for _, domain := range sortedOperationDomains(operationsByDomain) {
		fresh, err := r.nextSnapshot(ctx)
		if err != nil {
			return err
		}
		staleWithFresh := func(stale *PlanStaleError) error {
			r.pendingSnapshot = fresh
			return stale
		}
		gate, err := NewDomainGate(plan.ConvergenceID, fresh, r.desiredDomainUnion(), r.allowedCPUs(), plan.Witnesses)
		if err != nil {
			return err
		}
		priorGrowTargetByRel := make(map[string]machine.CPUSet, len(operationsByDomain[domain]))
		for _, operation := range operationsByDomain[domain] {
			current, ok := fresh.Entries[operation.Rel]
			if !ok {
				return staleWithFresh(&PlanStaleError{
					Rel: operation.Rel, Direction: operation.Direction, Resource: "authorization",
					Current: "missing", Target: operation.Target.CPUs.String(),
				})
			}
			delta := operation.Target.CPUs.Difference(current.CPUs)
			physicalEnvelope := r.targetByRel[operation.Rel].Difference(r.semanticTargetByRel[operation.Rel])
			authorized := fresh.DomainUnion[domain].
				Union(gate.AllowedEntering(domain)).
				Union(physicalEnvelope)
			if !delta.IsSubsetOf(authorized) {
				return staleWithFresh(&PlanStaleError{
					Rel: operation.Rel, Direction: operation.Direction, Resource: "authorization",
					Current: current.CPUs.String(), Target: operation.Target.CPUs.String(),
					Err: fmt.Errorf("fresh grow delta %s is not authorized for domain %q", delta.String(), domain),
				})
			}
			if operation.ParentRel != "" {
				parent, ok := fresh.Entries[operation.ParentRel]
				if !ok {
					return staleWithFresh(&PlanStaleError{
						Rel: operation.Rel, Direction: operation.Direction, Resource: "parent_cpuset",
						Current: "missing", Target: operation.Target.CPUs.String(),
					})
				}
				parentCovers := operation.Target.CPUs.IsSubsetOf(parent.CPUs)
				if plannedParent, planned := priorGrowTargetByRel[operation.ParentRel]; planned {
					parentCovers = parentCovers || operation.Target.CPUs.IsSubsetOf(plannedParent)
				}
				if !parentCovers {
					return staleWithFresh(&PlanStaleError{
						Rel: operation.Rel, Direction: operation.Direction, Resource: "parent_cpuset",
						Current: parent.CPUs.String(), Target: operation.Target.CPUs.String(),
						Err: fmt.Errorf("fresh parent %q cpuset does not cover grow target", operation.ParentRel),
					})
				}
			}
			priorGrowTargetByRel[operation.Rel] = operation.Target.CPUs.Clone()
		}
	}
	return nil
}

func sortedOperationDomains(operations map[DomainID][]PlanOperation) []DomainID {
	domains := make([]DomainID, 0, len(operations))
	for domain := range operations {
		domains = append(domains, domain)
	}
	sort.Slice(domains, func(i, j int) bool { return domains[i] < domains[j] })
	return domains
}

func drainFrontier(plan PhasePlan) (PhasePlan, error) {
	if len(plan.Operations) == 0 {
		return plan, nil
	}
	remaining := plan.CostUpperBound.Operations
	if remaining <= 0 || len(plan.Operations) > remaining {
		return PhasePlan{}, fmt.Errorf("%w: drain frontier limit=%d operations=%d",
			ErrPlanOperationBudgetExceeded, remaining, len(plan.Operations))
	}
	depthByRel, _ := buildSnapshotDepthByRel(plan.Base, nil)
	first := plan.Operations[0]
	depth := depthByRel[first.Rel]
	end := 1
	remaining--
	for end < len(plan.Operations) {
		if remaining == 0 {
			return PhasePlan{}, fmt.Errorf("%w: drain frontier exhausted after=%d", ErrPlanOperationBudgetExceeded, end)
		}
		remaining--
		operation := plan.Operations[end]
		if operation.Direction != first.Direction || depthByRel[operation.Rel] != depth {
			break
		}
		end++
	}
	plan.Operations = append([]PlanOperation(nil), plan.Operations[:end]...)
	plan.CostUpperBound.Operations = len(plan.Operations)
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}
	return plan, nil
}

func rebaseDrainPlan(plan PhasePlan, fresh *CompleteSnapshot, dag *TopoDAG, budget *BudgetTracker) (PhasePlan, error) {
	if fresh == nil {
		return PhasePlan{}, errors.New("cannot rebase drain plan without fresh snapshot")
	}
	if budget == nil {
		return PhasePlan{}, errors.New("cannot rebase drain plan without convergence budget")
	}
	targets := make(map[string]CPUSetTarget, len(fresh.Entries))
	for rel, entry := range fresh.Entries {
		target, ok := plan.TargetByRel[rel]
		if !ok {
			target = CPUSetTarget{CPUs: entry.CPUs.Clone(), Mems: entry.Mems}
		}
		targets[rel] = target
	}
	for rel := range plan.TargetByRel {
		if _, ok := fresh.Entries[rel]; !ok {
			return PhasePlan{}, &PlanStaleError{
				Rel: rel, Direction: WriteShrink, Resource: "snapshot",
				Current: "missing", Target: plan.TargetByRel[rel].CPUs.String(),
			}
		}
	}
	for rel := range targets {
		if relInDormantSubtree(rel, plan.DormantRels) {
			delete(targets, rel)
		}
	}
	depthByRel, relOrder := buildSnapshotDepthByRel(fresh, nil)
	domainByRel, parentByRel := buildPlannerRelations(fresh, dag, depthByRel, relOrder.relsAsc, relOrder.childRelsByRel, nil)
	postProcessPhaseOperationTargets(plan.Kind, plan.AllowEmptyTarget, plan.Capabilities, targets, fresh)
	if err := propagatePhaseTargetEnvelope(targets, parentByRel, depthByRel, relOrder.relsDesc); err != nil {
		return PhasePlan{}, err
	}
	if !plan.AllowEmptyTarget {
		if err := propagateControlledPhaseTargetEnvelope(targets, dag); err != nil {
			return PhasePlan{}, err
		}
		clampReclaimNUMABucketPhaseMems(targets, dag, nil)
	}
	operationCount, err := countPlanOperations(PhaseDrain, plan.Capabilities, targets, fresh, dag, nil)
	if err != nil {
		return PhasePlan{}, err
	}
	if err := budget.ConsumePlanOperations(operationCount); err != nil {
		return PhasePlan{}, err
	}
	plan.Base = fresh
	plan.TargetByRel = targets
	plan.Operations = buildPlanOperations(
		plan.Kind, plan.AllowEmptyTarget, plan.Capabilities,
		targets, fresh, depthByRel, domainByRel, parentByRel, dag, operationCount, nil, nil,
	)
	plan.CostUpperBound.Operations = len(plan.Operations)
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}
	return plan, nil
}

func accumulateDrainTransfers(
	released map[DomainID]map[DomainID]machine.CPUSet,
	graph map[DomainID]map[DomainID]machine.CPUSet,
	batch map[DomainID]machine.CPUSet,
) {
	for source, destinations := range graph {
		for destination, cpus := range destinations {
			drained := cpus.Intersection(batch[source])
			if drained.IsEmpty() {
				continue
			}
			if released[source] == nil {
				released[source] = make(map[DomainID]machine.CPUSet)
			}
			released[source][destination] = released[source][destination].Union(drained)
		}
	}
}

func verifiedDrainProgressRels(
	before, after *CompleteSnapshot,
	targets map[string]CPUSetTarget,
) []string {
	if before == nil || after == nil {
		return nil
	}
	changed := make([]string, 0)
	for rel, previous := range before.Entries {
		current, ok := after.Entries[rel]
		target, targeted := targets[rel]
		if !ok || !targeted {
			continue
		}
		cpusProgressed := target.CPUs.IsSubsetOf(current.CPUs) &&
			current.CPUs.IsSubsetOf(previous.CPUs) &&
			!current.CPUs.Equals(previous.CPUs)
		memsProgressed := current.Mems == target.Mems && previous.Mems != target.Mems
		if cpusProgressed || memsProgressed {
			changed = append(changed, rel)
		}
	}
	sort.Strings(changed)
	return changed
}

func (r *coordinatorRound) recomputeBlocked(snapshot *CompleteSnapshot) {
	r.blocked = make(map[DomainID]machine.CPUSet)
	desired := r.desiredDomainUnion()
	for destination, target := range desired {
		for source, observed := range snapshot.DomainUnion {
			if source != destination {
				r.blocked[destination] = r.blocked[destination].Union(observed.Intersection(target))
			}
		}
	}
}

func cgroupRootPathCPUSet() string {
	return cgcommon.GetCgroupRootPath(cgcommon.CgroupSubsysCPUSet)
}
