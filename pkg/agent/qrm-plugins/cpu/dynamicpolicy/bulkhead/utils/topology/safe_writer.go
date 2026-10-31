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
	"path/filepath"
	"sort"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type AppliedPlanOperation struct {
	PlanID                string
	Rel                   string
	Direction             WriteDirection
	Target                CPUSetTarget
	Observed              CPUSetTarget
	Resource              HierarchyOperation
	PhysicalImpact        PhysicalImpact
	LogicalOperationIndex int
	Phase                 PhaseKind
}

type PhysicalImpact string

const (
	PhysicalImpactNone      PhysicalImpact = ""
	PhysicalImpactConfirmed PhysicalImpact = "confirmed"
	PhysicalImpactUncertain PhysicalImpact = "uncertain"
)

type safeCPUSetWriter struct {
	driver                 HierarchyDriver
	budget                 *BudgetTracker
	adjustmentBudget       *AdjustmentBudget
	adjustmentReservation  *AdjustmentWriteReservation
	recoveryContextFactory func(int) (context.Context, context.CancelFunc, error)
	res                    *ConvergenceResult
	physicalWriteAttempts  *int
	dormantProofs          map[string]DormantLeafProof
}

type stableLiveChildren struct {
	cpus  machine.CPUSet
	mems  machine.CPUSet
	refs  []ChildRef
	byRel map[string]EntryState
}

func newSafeCPUSetWriter(driver HierarchyDriver, budget *BudgetTracker, res *ConvergenceResult) safeCPUSetWriter {
	physicalWriteAttempts := 0
	return safeCPUSetWriter{
		driver:                 driver,
		budget:                 budget,
		recoveryContextFactory: newAdjustmentRecoveryContext,
		res:                    res,
		physicalWriteAttempts:  &physicalWriteAttempts,
	}
}

func (w safeCPUSetWriter) withAdjustmentBudget(budget *AdjustmentBudget) safeCPUSetWriter {
	w.adjustmentBudget = budget
	return w
}

// execute runs a PhasePlan's operations with the preflight-then-mutate contract:
//
//  1. Freeze: scan all live children once and validate every operation against
//     the frozen snapshot BEFORE any physical write. A preflight failure on any
//     operation means ZERO writes land — the plan is aborted entirely.
//  2. Reserve: consume the write-budget reservation for the estimated IO cost.
//  3. Forward: apply operations in order, verifying each child union after the
//     write. A verification failure triggers a full-prefix rollback of all
//     previously applied operations in this plan.
//  4. Settle: re-snapshot the affected rels to confirm the post-write state.
//
// This is the safety boundary for all cgroup cpuset writes in the bulkhead
// subsystem. The preflight freeze prevents "write to A, then discover B is
// stale" partial-application bugs; the rollback prevents drifting cgroup state
// after a mid-plan failure.
func (w safeCPUSetWriter) execute(ctx context.Context, plan PhasePlan) error {
	if plan.PlanID == "" || canonicalExecutionPlanID(plan) != plan.PlanID {
		return fmt.Errorf("phase writer requires canonical plan id")
	}
	for _, operation := range plan.Operations {
		if operation.PlanID != plan.PlanID {
			return fmt.Errorf("phase writer rejected operation %q owned by plan id %q, current %q",
				operation.Rel, operation.PlanID, plan.PlanID)
		}
		if operation.WriteMems && !operation.OwnsMems {
			return fmt.Errorf("phase writer rejected mems write for unowned rel %q", operation.Rel)
		}
	}
	if w.driver == nil {
		return fmt.Errorf("phase writer requires hierarchy driver")
	}
	if w.budget == nil {
		return fmt.Errorf("phase writer requires convergence budget")
	}
	failClosedRels := safeWriterFailClosedRels(plan)
	stableChildUnion, err := w.scanStableOperationChildren(
		ctx, plan.Operations, plan.Capabilities, failClosedRels)
	if err != nil {
		return err
	}
	ioOperations := 0
	for _, operation := range plan.Operations {
		ioOperations = saturatingAdd(ioOperations,
			estimateFinalPreflightAndMutationHierarchyIO(operation, len(stableChildUnion[operation.Rel].refs)))
		ioOperations = saturatingAdd(ioOperations, w.dormantProofCountUnder(operation.Rel))
	}
	ioOperations = saturatingAdd(ioOperations, len(w.dormantProofs))
	driver, err := newStrictReservedHierarchyDriver(ctx, w.driver, w.budget, ioOperations)
	if err != nil {
		return err
	}
	w.driver = driver

	// Freeze and validate the complete operation set before the first mutation.
	// In particular, a later operation becoming stale must not leave an earlier
	// operation partially applied.
	preflightChildren := make(map[string]stableLiveChildren, len(plan.Operations))
	precedingOperations := make(map[string]PlanOperation, len(plan.Operations))
	for _, operation := range plan.Operations {
		children, err := scanFrozenLiveChildrenOnce(
			ctx, w.driver, operation, stableChildUnion[operation.Rel],
			operation.Direction == WriteShrink && operation.WriteMems, failClosedRels)
		if err != nil {
			return err
		}
		children = projectPrecedingChildTargets(operation.Rel, children, precedingOperations)
		preflightChildren[operation.Rel] = children
		if err := w.precheckOperation(
			ctx, operation, preflightChildren, plan.Capabilities, precedingOperations); err != nil {
			return err
		}
		precedingOperations[operation.Rel] = operation
	}

	reservation, err := w.adjustmentBudget.ReserveExecution(planExecutionReservationCost(plan))
	if err != nil {
		return err
	}
	w.adjustmentReservation = reservation
	forwardStart, rollbackStart := 0, 0
	if w.res != nil {
		forwardStart = w.res.forwardWriteAttempts
		rollbackStart = w.res.rollbackWriteAttempts
	}
	defer func() {
		if w.res == nil {
			_ = reservation.settleTotals(0, 0)
			return
		}
		_ = reservation.settleTotals(
			w.res.forwardWriteAttempts-forwardStart,
			w.res.rollbackWriteAttempts-rollbackStart,
		)
	}()

	if len(plan.Operations) > 0 {
		if err := w.revalidateDormantProofs(ctx, ""); err != nil {
			return err
		}
	}
	for _, operation := range plan.Operations {
		// Recheck only the dormant envelopes that this operation's ancestor
		// containment depends on. This keeps the authorization close to the
		// corresponding write without multiplying activity scans for unrelated
		// leaves.
		if err := w.revalidateDormantProofs(ctx, operation.Rel); err != nil {
			return err
		}
		if w.res != nil {
			w.res.Attempted++
		}
		wroteMems := false
		wroteCPUs := false
		if operation.WriteMems && operation.ExpectedCurrent.Mems != operation.Target.Mems {
			if err := w.recordForwardPhysicalWriteAttempt(ctx, PhysicalWriteCost{MemsWrites: 1}); err != nil {
				return err
			}
			if err := w.driver.WriteMems(ctx, operation.Rel, operation.ExpectedIdentity, operation.Target.Mems); err != nil {
				if w.res != nil {
					w.res.Failed++
				}
				return w.classifyWriteError(
					err, plan.Kind, HierarchyOperationWriteMems, operation, "cpuset.mems",
					operation.ExpectedCurrent.Mems, operation.Target.Mems)
			}
			wroteMems = true
		}
		if !operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs) {
			if err := w.recordForwardPhysicalWriteAttempt(ctx, PhysicalWriteCost{CPUSetWrites: 1}); err != nil {
				if rollbackErr := w.rollbackOperationWithRecovery(operation, wroteCPUs, wroteMems); rollbackErr != nil {
					return executionWithRollbackError(err, rollbackErr)
				}
				return err
			}
			if err := w.driver.WriteCPUs(ctx, operation.Rel, operation.ExpectedIdentity, operation.Target.CPUs); err != nil {
				if w.res != nil {
					w.res.Failed++
				}
				var writeErr error
				if w.driver.Classify(err, HierarchyOperationWriteCPUs) != HierarchyErrorStale {
					writeErr = phaseWriteError(
						err, plan.Kind, operation, "cpuset.cpus",
						operation.ExpectedCurrent.CPUs.String(), operation.Target.CPUs.String(),
					)
				} else {
					current := operation.ExpectedCurrent.CPUs.String()
					if wroteMems {
						if applied, readErr := w.readAppliedObservation(ctx, operation); readErr == nil {
							current = applied.Observed.CPUs.String()
						}
					}
					writeErr = &PlanStaleError{
						Rel: operation.Rel, Direction: operation.Direction, Resource: "cpuset.cpus",
						Current: current, Target: operation.Target.CPUs.String(), Err: err,
					}
				}
				if rollbackErr := w.rollbackOperationWithRecovery(operation, wroteCPUs, wroteMems); rollbackErr != nil {
					return executionWithRollbackError(writeErr, rollbackErr)
				}
				return writeErr
			}
			wroteCPUs = true
		}
		applied, err := w.readAfterWrite(ctx, operation)
		if w.res != nil && applied.PlanID != "" {
			w.res.Journal = append(w.res.Journal, applied)
		}
		if err != nil {
			if w.res != nil {
				w.res.Failed++
			}
			if rollbackErr := w.rollbackOperationWithRecovery(operation, wroteCPUs, wroteMems); rollbackErr != nil {
				return executionWithRollbackError(err, rollbackErr)
			}
			return err
		}
		if w.res != nil {
			w.res.Applied++
		}
	}
	return nil
}

func planExecutionReservationCost(plan PhasePlan) ExecutionReservationCost {
	var cost ExecutionReservationCost
	for _, operation := range plan.Operations {
		forward := physicalWriteCost(
			operation.ExpectedCurrent, operation.Target, operation.WriteMems)
		rollback := physicalWriteCost(
			operation.Target, operation.ExpectedCurrent, operation.WriteMems)
		cost.Forward = addPhysicalWriteCost(cost.Forward, forward)
		cost.Rollback = addPhysicalWriteCost(cost.Rollback, rollback)
	}
	return cost
}

func executionWithRollbackError(executionErr, rollbackErr error) error {
	return newExecutionRollbackError(executionErr, rollbackErr)
}

func (w safeCPUSetWriter) rollbackOperationWithRecovery(
	operation PlanOperation,
	wroteCPUs, wroteMems bool,
) error {
	rollbackWrites := 0
	if wroteCPUs {
		rollbackWrites++
	}
	if wroteMems {
		rollbackWrites++
	}
	recoveryCtx, cancelRecovery, err := newAdjustmentRecoveryContext(rollbackWrites)
	if err != nil {
		return err
	}
	defer cancelRecovery()
	return w.rollbackOperation(recoveryCtx, operation, wroteCPUs, wroteMems)
}

func (w safeCPUSetWriter) rollbackOperation(
	ctx context.Context,
	operation PlanOperation,
	wroteCPUs, wroteMems bool,
) error {
	var rollbackErr error
	if wroteCPUs {
		if err := w.recordRollbackPhysicalWriteAttempt(ctx, PhysicalWriteCost{CPUSetWrites: 1}); err != nil {
			rollbackErr = utilerrors.NewAggregate([]error{rollbackErr, err})
		} else if err := w.driver.WriteCPUs(
			ctx, operation.Rel, operation.ExpectedIdentity, operation.ExpectedCurrent.CPUs,
		); err != nil {
			rollbackErr = utilerrors.NewAggregate([]error{
				rollbackErr,
				fmt.Errorf("rollback cpuset.cpus for %q: %w", operation.Rel, err),
			})
		}
	}
	if wroteMems {
		if err := w.recordRollbackPhysicalWriteAttempt(ctx, PhysicalWriteCost{MemsWrites: 1}); err != nil {
			rollbackErr = utilerrors.NewAggregate([]error{rollbackErr, err})
		} else if err := w.driver.WriteMems(
			ctx, operation.Rel, operation.ExpectedIdentity, operation.ExpectedCurrent.Mems,
		); err != nil {
			rollbackErr = utilerrors.NewAggregate([]error{
				rollbackErr,
				fmt.Errorf("rollback cpuset.mems for %q: %w", operation.Rel, err),
			})
		}
	}
	return rollbackErr
}

func (w safeCPUSetWriter) dormantProofCountUnder(ancestor string) int {
	count := 0
	for root := range w.dormantProofs {
		if ancestor == "" || isRelAtOrUnder(root, ancestor) {
			count++
		}
	}
	return count
}

// revalidateDormantProofs verifies the inactive cgroup v1 leaves whose
// physical envelopes authorize ancestor containment. An empty ancestor checks
// the complete proof set before the first mutation; a non-empty ancestor
// narrows the check to envelopes used by that operation. External task attach
// does not share a lifecycle lock with this writer, so every mismatch is stale
// evidence that must abort the plan and trigger replanning; the writer must
// never continue by retrying the old physical envelope.
func (w safeCPUSetWriter) revalidateDormantProofs(ctx context.Context, ancestor string) error {
	reader, ok := w.driver.(hierarchyActivityReader)
	if !ok && len(w.dormantProofs) > 0 {
		return &PlanStaleError{
			Direction: WritePublish, Resource: "dormant_subtree",
			Current: "activity proof unavailable", Target: "complete inactive child closure",
		}
	}
	roots := make([]string, 0, len(w.dormantProofs))
	for root := range w.dormantProofs {
		roots = append(roots, root)
	}
	sort.Strings(roots)
	for _, root := range roots {
		if ancestor != "" && !isRelAtOrUnder(root, ancestor) {
			continue
		}
		proof := w.dormantProofs[root]
		entry, err := reader.ReadEntryWithActivity(ctx, root, proof.Identity)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
				isConvergenceBudgetError(err) ||
				w.driver.Classify(err, HierarchyOperationRead) != HierarchyErrorStale {
				return err
			}
			return &PlanStaleError{
				Rel: root, Direction: WritePublish, Resource: "dormant_subtree",
				Current: "unavailable", Target: "complete inactive child closure", Err: err,
			}
		}
		if entry.Identity != proof.Identity || !entry.Activity.Inactive() ||
			!entry.CPUs.Equals(proof.ObservedCPUs) {
			return &PlanStaleError{
				Rel: root, Direction: WritePublish, Resource: "dormant_subtree",
				Current: fmt.Sprintf("identity=%v cpus=%s activity=%+v",
					entry.Identity, entry.CPUs.String(), entry.Activity),
				Target: fmt.Sprintf("identity=%v cpus=%s inactive", proof.Identity, proof.ObservedCPUs.String()),
			}
		}
	}
	return nil
}

func estimateStableChildScanHierarchyIO(scans, childMemberships int) int {
	if scans < 0 || childMemberships < 0 {
		return 0
	}
	// Two child-list observations bracket one entry read per membership.
	return saturatingAdd(saturatingMultiply(2, scans), childMemberships)
}

func estimateFinalPreflightAndMutationHierarchyIO(operation PlanOperation, childMemberships int) int {
	operations := 2 // local precheck and post-write readback
	if operation.ParentRel != "" {
		operations++ // live parent identity/containment precheck
	}
	if !operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs) {
		operations += 2 // cpuset.cpus forward write and worst-case rollback
	}
	if operation.WriteMems && operation.ExpectedCurrent.Mems != operation.Target.Mems {
		operations += 2 // cpuset.mems forward write and worst-case rollback
	}
	// Every operation gets one final child-set freeze before any operation is
	// written. Each frozen child can require a read and an identity stat when an
	// unavailable v2 interface is skipped.
	operations = saturatingAdd(operations, saturatingAdd(2, saturatingMultiply(2, childMemberships)))
	return operations
}

func (w safeCPUSetWriter) classifyWriteError(
	err error,
	phase PhaseKind,
	hierarchyOperation HierarchyOperation,
	operation PlanOperation,
	resource, current, target string,
) error {
	if w.driver.Classify(err, hierarchyOperation) != HierarchyErrorStale {
		return phaseWriteError(err, phase, operation, resource, current, target)
	}
	return &PlanStaleError{
		Rel: operation.Rel, Direction: operation.Direction, Resource: resource,
		Current: current, Target: target, Err: err,
	}
}

func phaseWriteError(
	err error,
	phase PhaseKind,
	operation PlanOperation,
	resource, current, target string,
) error {
	return fmt.Errorf("phase write failed: phase=%s rel=%q direction=%s resource=%s current=%s target=%s: %w",
		phase, operation.Rel, operation.Direction, resource, current, target, err)
}

func (w safeCPUSetWriter) scanStableOperationChildren(
	ctx context.Context,
	operations []PlanOperation,
	capabilities HierarchyCapabilities,
	failClosedRels map[string]struct{},
) (map[string]stableLiveChildren, error) {
	driver := w.driver
	if wrapped, ok := driver.(*budgetedHierarchyDriver); !ok || wrapped.budget != w.budget {
		driver = NewBudgetedHierarchyDriver(driver, w.budget)
	}
	stableChildren := make(map[string]stableLiveChildren)
	for _, operation := range operations {
		skipCPUCheck := isConfiguredInheritanceClear(operation, capabilities)
		checkMems := operation.Direction == WriteShrink && operation.WriteMems
		children, err := scanStableLiveChildren(
			ctx, driver, operation.Rel, checkMems, failClosedRels)
		if err != nil {
			if driver.Classify(err, HierarchyOperationList) == HierarchyErrorStale {
				return nil, &PlanStaleError{
					Rel: operation.Rel, Direction: operation.Direction, Resource: "hierarchy",
					Err: err,
				}
			}
			return nil, err
		}
		if operation.Direction == WriteShrink &&
			!skipCPUCheck && !children.cpus.IsSubsetOf(operation.Target.CPUs) {
			return nil, &PlanStaleError{
				Rel: operation.Rel, Direction: operation.Direction, Resource: "child_union",
				Current: children.cpus.String(), Target: operation.Target.CPUs.String(),
				Err: fmt.Errorf("live child union is outside shrink target"),
			}
		}
		stableChildren[operation.Rel] = children
	}
	return stableChildren, nil
}

func scanFrozenLiveChildrenOnce(
	ctx context.Context,
	driver HierarchyDriver,
	operation PlanOperation,
	frozen stableLiveChildren,
	readMems bool,
	failClosedRels map[string]struct{},
) (stableLiveChildren, error) {
	before, err := driver.ListChildren(ctx, operation.Rel)
	if err != nil {
		return stableLiveChildren{}, err
	}
	frozenFingerprint := ChildrenFingerprint(frozen.refs)
	if currentFingerprint := ChildrenFingerprint(before); currentFingerprint != frozenFingerprint {
		return stableLiveChildren{}, &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: "children",
			Current: currentFingerprint, Target: frozenFingerprint,
			Err: fmt.Errorf("live children changed during final preflight"),
		}
	}
	children := stableLiveChildren{
		cpus:  machine.NewCPUSet(),
		mems:  machine.NewCPUSet(),
		refs:  before,
		byRel: make(map[string]EntryState, len(before)),
	}
	for _, child := range before {
		childRel := filepath.Join(operation.Rel, child.Name)
		entry, readErr := driver.ReadEntry(ctx, childRel)
		if readErr != nil {
			skip, proofErr := proveSafeUnavailableChildSkip(
				ctx, driver, childRel, child.Identity, readErr, failClosedRels)
			if proofErr != nil {
				return stableLiveChildren{}, proofErr
			}
			if skip {
				continue
			}
			return stableLiveChildren{}, readErr
		}
		if entry.Identity != child.Identity {
			return stableLiveChildren{}, &PlanStaleError{
				Rel: operation.Rel, Direction: operation.Direction, Resource: "child_identity",
				Current: fmt.Sprint(entry.Identity), Target: fmt.Sprint(child.Identity),
				Err: fmt.Errorf("live child identity changed during final preflight"),
			}
		}
		children.cpus = children.cpus.Union(entry.CPUs)
		children.byRel[childRel] = entry
		if readMems {
			childMems, parseErr := machine.Parse(entry.Mems)
			if parseErr != nil {
				return stableLiveChildren{}, fmt.Errorf("parse live child %q cpuset.mems=%q: %w",
					childRel, entry.Mems, parseErr)
			}
			children.mems = children.mems.Union(childMems)
		}
	}
	after, err := driver.ListChildren(ctx, operation.Rel)
	if err != nil {
		return stableLiveChildren{}, err
	}
	if currentFingerprint := ChildrenFingerprint(after); currentFingerprint != frozenFingerprint {
		return stableLiveChildren{}, &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: "children",
			Current: currentFingerprint, Target: frozenFingerprint,
			Err: fmt.Errorf("live children changed during final preflight"),
		}
	}
	return children, nil
}

func projectPrecedingChildTargets(
	parentRel string,
	children stableLiveChildren,
	precedingOperations map[string]PlanOperation,
) stableLiveChildren {
	projected := stableLiveChildren{
		cpus:  machine.NewCPUSet(),
		mems:  machine.NewCPUSet(),
		refs:  children.refs,
		byRel: make(map[string]EntryState, len(children.byRel)),
	}
	for rel, entry := range children.byRel {
		if operation, ok := precedingOperations[rel]; ok && filepath.Dir(rel) == parentRel {
			entry.CPUs = operation.Target.CPUs.Clone()
			if operation.WriteMems {
				entry.Mems = operation.Target.Mems
			}
		}
		projected.byRel[rel] = entry
		projected.cpus = projected.cpus.Union(entry.CPUs)
		if entry.Mems != "" {
			if mems, err := machine.Parse(entry.Mems); err == nil {
				projected.mems = projected.mems.Union(mems)
			}
		}
	}
	return projected
}

func scanStableLiveChildren(
	ctx context.Context,
	driver HierarchyDriver,
	rel string,
	readMems bool,
	failClosedRels map[string]struct{},
) (stableLiveChildren, error) {
	for {
		before, err := driver.ListChildren(ctx, rel)
		if err != nil {
			if driver.Classify(err, HierarchyOperationList) == HierarchyErrorStale {
				// The relation being scanned, rather than one of its children,
				// disappeared. Re-listing cannot recover the frozen generation;
				// return the stale evidence so the caller can replan.
				return stableLiveChildren{}, err
			}
			return stableLiveChildren{}, err
		}
		childUnion := machine.NewCPUSet()
		childMemsUnion := machine.NewCPUSet()
		childEntries := make(map[string]EntryState, len(before))
		stale := false
		for _, child := range before {
			childRel := filepath.Join(rel, child.Name)
			entry, readErr := driver.ReadEntry(ctx, childRel)
			if readErr != nil {
				skip, proofErr := proveSafeUnavailableChildSkip(
					ctx, driver, childRel, child.Identity, readErr, failClosedRels)
				if proofErr != nil {
					return stableLiveChildren{}, proofErr
				}
				if skip {
					continue
				}
				if driver.Classify(readErr, HierarchyOperationRead) == HierarchyErrorStale {
					stale = true
					break
				}
				return stableLiveChildren{}, readErr
			}
			if entry.Identity != child.Identity {
				stale = true
				break
			}
			childEntries[childRel] = entry
			if readMems {
				childMems, parseErr := machine.Parse(entry.Mems)
				if parseErr != nil {
					// Unparseable live child mems cannot prove a safe parent shrink, so fail-closed.
					return stableLiveChildren{}, fmt.Errorf("parse live child %q cpuset.mems=%q: %w",
						childRel, entry.Mems, parseErr)
				}
				childMemsUnion = childMemsUnion.Union(childMems)
			}
			childUnion = childUnion.Union(entry.CPUs)
		}
		if stale {
			continue
		}
		after, err := driver.ListChildren(ctx, rel)
		if err != nil {
			if driver.Classify(err, HierarchyOperationList) == HierarchyErrorStale {
				// A stale second list means the scanned relation vanished after
				// the first observation. Child churn is retried above, but this
				// parent-generation loss must invalidate the plan.
				return stableLiveChildren{}, err
			}
			return stableLiveChildren{}, err
		}
		if ChildrenFingerprint(before) != ChildrenFingerprint(after) {
			continue
		}
		return stableLiveChildren{cpus: childUnion, mems: childMemsUnion, refs: before, byRel: childEntries}, nil
	}
}

// strictReservedHierarchyDriver consumes only the slots atomically reserved by
// safeCPUSetWriter. Exhaustion is a fail-closed accounting bug; it must never
// fall back to ordinary per-call charging after mutation preflight begins.
type strictReservedHierarchyDriver struct {
	HierarchyDriver
	budget    *BudgetTracker
	remaining int
}

func newStrictReservedHierarchyDriver(
	ctx context.Context,
	driver HierarchyDriver,
	budget *BudgetTracker,
	operations int,
) (HierarchyDriver, error) {
	if err := budget.ReserveHierarchyIOOperationsWithContext(ctx, operations); err != nil {
		return nil, err
	}
	if wrapped, ok := driver.(*budgetedHierarchyDriver); ok && wrapped.budget == budget {
		driver = wrapped.driver
	}
	return &strictReservedHierarchyDriver{
		HierarchyDriver: driver,
		budget:          budget,
		remaining:       operations,
	}, nil
}

func (d *strictReservedHierarchyDriver) consume(ctx context.Context) error {
	if isAdjustmentRecoveryContext(ctx) {
		if err := ctx.Err(); err != nil {
			return err
		}
	} else {
		if err := d.budget.checkContextDeadline(ctx); err != nil {
			return err
		}
	}
	if d.remaining <= 0 {
		return fmt.Errorf("%w: prepaid hierarchy I/O exhausted",
			ErrHierarchyIOOperationBudgetExceeded)
	}
	d.remaining--
	return nil
}

func (d *strictReservedHierarchyDriver) Roots(ctx context.Context) ([]RootRef, error) {
	if err := d.consume(ctx); err != nil {
		return nil, err
	}
	return d.HierarchyDriver.Roots(ctx)
}

func (d *strictReservedHierarchyDriver) StatIdentity(ctx context.Context, rel string) (CgroupIdentity, error) {
	if err := d.consume(ctx); err != nil {
		return CgroupIdentity{}, err
	}
	return d.HierarchyDriver.StatIdentity(ctx, rel)
}

func (d *strictReservedHierarchyDriver) ReadEntry(ctx context.Context, rel string) (EntryState, error) {
	if err := d.consume(ctx); err != nil {
		return EntryState{}, err
	}
	return d.HierarchyDriver.ReadEntry(ctx, rel)
}

func (d *strictReservedHierarchyDriver) ReadEntryWithActivity(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
) (EntryState, error) {
	if err := d.consume(ctx); err != nil {
		return EntryState{}, err
	}
	if reader, ok := d.HierarchyDriver.(budgetedHierarchyActivityReader); ok {
		return reader.readEntryWithActivityAndBudget(ctx, rel, expected, d.budget)
	}
	reader, ok := d.HierarchyDriver.(hierarchyActivityReader)
	if !ok {
		return EntryState{}, fmt.Errorf("hierarchy driver does not support cgroup activity proofs")
	}
	return reader.ReadEntryWithActivity(ctx, rel, expected)
}

func (d *strictReservedHierarchyDriver) ListChildren(ctx context.Context, rel string) ([]ChildRef, error) {
	if err := d.consume(ctx); err != nil {
		return nil, err
	}
	if driver, ok := d.HierarchyDriver.(interface {
		listChildrenWithBudget(context.Context, string, *BudgetTracker) ([]ChildRef, error)
	}); ok {
		return driver.listChildrenWithBudget(ctx, rel, d.budget)
	}
	children, err := d.HierarchyDriver.ListChildren(ctx, rel)
	if err != nil {
		return nil, err
	}
	depth := childDepth(rel)
	for _, child := range children {
		if err := d.budget.checkContextDeadline(ctx); err != nil {
			return nil, err
		}
		if err := d.budget.VisitNode(filepath.Join(rel, child.Name), child.Identity, depth); err != nil {
			return nil, err
		}
	}
	return children, nil
}

func (d *strictReservedHierarchyDriver) WriteCPUs(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
	cpus machine.CPUSet,
) error {
	if err := d.consume(ctx); err != nil {
		return err
	}
	return d.HierarchyDriver.WriteCPUs(ctx, rel, expected, cpus)
}

func (d *strictReservedHierarchyDriver) WriteMems(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
	mems string,
) error {
	if err := d.consume(ctx); err != nil {
		return err
	}
	return d.HierarchyDriver.WriteMems(ctx, rel, expected, mems)
}

func scanLiveChildrenOnce(
	ctx context.Context,
	driver HierarchyDriver,
	operation PlanOperation,
	failClosedRels map[string]struct{},
) (stableLiveChildren, error) {
	before, err := driver.ListChildren(ctx, operation.Rel)
	if err != nil {
		return stableLiveChildren{}, err
	}
	children := stableLiveChildren{
		cpus: machine.NewCPUSet(),
		mems: machine.NewCPUSet(),
		refs: before,
	}
	for _, child := range before {
		childRel := filepath.Join(operation.Rel, child.Name)
		entry, readErr := driver.ReadEntry(ctx, childRel)
		if readErr != nil {
			skip, proofErr := proveSafeUnavailableChildSkip(
				ctx, driver, childRel, child.Identity, readErr, failClosedRels)
			if proofErr != nil {
				return stableLiveChildren{}, proofErr
			}
			if skip {
				continue
			}
			return stableLiveChildren{}, readErr
		}
		if entry.Identity != child.Identity {
			return stableLiveChildren{}, &PlanStaleError{
				Rel: operation.Rel, Direction: operation.Direction, Resource: "child_identity",
				Current: fmt.Sprint(entry.Identity), Target: fmt.Sprint(child.Identity),
				Err: fmt.Errorf("live child identity changed"),
			}
		}
		childMems, parseErr := machine.Parse(entry.Mems)
		if parseErr != nil {
			return stableLiveChildren{}, fmt.Errorf("parse live child %q cpuset.mems=%q: %w",
				childRel, entry.Mems, parseErr)
		}
		children.cpus = children.cpus.Union(entry.CPUs)
		children.mems = children.mems.Union(childMems)
	}
	after, err := driver.ListChildren(ctx, operation.Rel)
	if err != nil {
		return stableLiveChildren{}, err
	}
	if ChildrenFingerprint(before) != ChildrenFingerprint(after) {
		return stableLiveChildren{}, &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: "children",
			Current: ChildrenFingerprint(after), Target: ChildrenFingerprint(before),
			Err: fmt.Errorf("live children changed before shrink"),
		}
	}
	return children, nil
}

func safeWriterFailClosedRels(plan PhasePlan) map[string]struct{} {
	rels := make(map[string]struct{}, len(plan.Operations)+len(plan.ControlledRels))
	for _, rel := range plan.ControlledRels {
		rels[rel] = struct{}{}
	}
	for _, operation := range plan.Operations {
		rels[operation.Rel] = struct{}{}
	}
	for _, rel := range plan.FailClosedRoots {
		rels[rel] = struct{}{}
	}
	return rels
}

func proveSafeUnavailableChildSkip(
	ctx context.Context,
	driver HierarchyDriver,
	rel string,
	expectedIdentity CgroupIdentity,
	readErr error,
	failClosedRels map[string]struct{},
) (bool, error) {
	if !errors.Is(readErr, ErrCgroupControllerUnavailable) {
		return false, nil
	}
	if !driver.Capabilities().EffectiveCPUSet {
		return false, readErr
	}
	if _, failClosed := failClosedRels[rel]; failClosed {
		return false, readErr
	}
	currentIdentity, err := driver.StatIdentity(ctx, rel)
	if err != nil {
		return false, err
	}
	if currentIdentity != expectedIdentity {
		return false, fmt.Errorf("%w: child rel=%q listed=%v current=%v",
			ErrCgroupIdentityChanged, rel, expectedIdentity, currentIdentity)
	}
	return true, nil
}

func (w safeCPUSetWriter) precheckOperation(
	ctx context.Context,
	operation PlanOperation,
	stableChildUnion map[string]stableLiveChildren,
	capabilities HierarchyCapabilities,
	precedingOperations map[string]PlanOperation,
) error {
	if operation.Direction != WriteShrink && operation.Direction != WriteGrow {
		return fmt.Errorf("unsupported plan operation direction %q", operation.Direction)
	}
	current, err := w.driver.ReadEntry(ctx, operation.Rel)
	if err != nil {
		return w.classifyHierarchyReadError(err, operation)
	}
	if current.Identity != operation.ExpectedIdentity {
		return fmt.Errorf("%w: planned operation rel=%q expected=%v current=%v",
			ErrCgroupIdentityChanged, operation.Rel, operation.ExpectedIdentity, current.Identity)
	}
	if err := validateLiveOperationDirection(operation, current, capabilities); err != nil {
		return err
	}
	if err := w.precheckOperationChildren(operation, stableChildUnion, capabilities); err != nil {
		return err
	}
	if operation.ParentRel != "" {
		parent, err := w.driver.ReadEntry(ctx, operation.ParentRel)
		if err != nil {
			return w.classifyHierarchyReadError(err, operation)
		}
		if parent.Identity != operation.ExpectedParentIdentity {
			return fmt.Errorf("%w: planned operation parent=%q expected=%v current=%v",
				ErrCgroupIdentityChanged, operation.ParentRel, operation.ExpectedParentIdentity, parent.Identity)
		}
		parentCPUs := parent.CPUs
		parentMems := parent.Mems
		if parentOperation, ok := precedingOperations[operation.ParentRel]; ok {
			parentCPUs = parentOperation.Target.CPUs
			if parentOperation.WriteMems {
				parentMems = parentOperation.Target.Mems
			}
		}
		if operation.Direction == WriteGrow && !operation.Target.CPUs.IsSubsetOf(parentCPUs) {
			return &PlanStaleError{
				Rel:       operation.Rel,
				Direction: operation.Direction,
				Resource:  "parent_cpuset.cpus",
				Current:   parentCPUs.String(),
				Target:    operation.Target.CPUs.String(),
				Err: fmt.Errorf("planned grow target outside live parent %q",
					operation.ParentRel),
			}
		}
		if operation.Direction == WriteGrow && operation.WriteMems {
			parsedParentMems, parentErr := machine.Parse(parentMems)
			targetMems, targetErr := machine.Parse(operation.Target.Mems)
			if parentErr != nil || targetErr != nil || !targetMems.IsSubsetOf(parsedParentMems) {
				return &PlanStaleError{
					Rel: operation.Rel, Direction: operation.Direction, Resource: "parent_cpuset.mems",
					Current: parentMems, Target: operation.Target.Mems,
					Err: fmt.Errorf("planned mems grow target outside live parent %q: parent_parse=%v target_parse=%v",
						operation.ParentRel, parentErr, targetErr),
				}
			}
		}
	}
	return nil
}

func (w safeCPUSetWriter) precheckOperationChildren(
	operation PlanOperation,
	stableChildUnion map[string]stableLiveChildren,
	capabilities HierarchyCapabilities,
) error {
	skipCPUCheck := isConfiguredInheritanceClear(operation, capabilities)
	if skipCPUCheck && !operation.WriteMems {
		// In v2, clearing configured only switches to inheritance without shrinking effective state; therefore,
		// a CPU-only clear skips CPU child containment checks, while concurrent mems writes still validate child mems.
		return nil
	}
	if operation.Direction == WriteShrink {
		children, ok := stableChildUnion[operation.Rel]
		if !ok {
			return fmt.Errorf("missing stable live child union for shrink rel=%q", operation.Rel)
		}
		if !skipCPUCheck && !children.cpus.IsSubsetOf(operation.Target.CPUs) {
			return &PlanStaleError{
				Rel: operation.Rel, Direction: operation.Direction, Resource: "child_union",
				Current: children.cpus.String(), Target: operation.Target.CPUs.String(),
				Err: fmt.Errorf("live child union is outside shrink target"),
			}
		}
		if operation.WriteMems {
			targetMems, parseErr := machine.Parse(operation.Target.Mems)
			if parseErr != nil || !children.mems.IsSubsetOf(targetMems) {
				return &PlanStaleError{
					Rel: operation.Rel, Direction: operation.Direction, Resource: "child_union_cpuset.mems",
					Current: children.mems.String(), Target: operation.Target.Mems,
					Err: fmt.Errorf("live child mems union is outside shrink target: target_parse=%v", parseErr),
				}
			}
		}
		return nil
	}
	return nil
}

func (w safeCPUSetWriter) readAfterWrite(ctx context.Context, operation PlanOperation) (AppliedPlanOperation, error) {
	applied, err := w.readAppliedObservation(ctx, operation)
	if err != nil {
		return AppliedPlanOperation{}, err
	}
	current := applied.Observed
	if !current.CPUs.Equals(operation.Target.CPUs) {
		return applied, &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: "post_write_cpuset.cpus",
			Current: current.CPUs.String(), Target: operation.Target.CPUs.String(),
			Err: fmt.Errorf("post-write observation differs from target"),
		}
	}
	if operation.OwnsMems {
		currentMems, currentMemsErr := machine.Parse(current.Mems)
		targetMems, targetMemsErr := machine.Parse(operation.Target.Mems)
		if currentMemsErr != nil || targetMemsErr != nil || !currentMems.Equals(targetMems) {
			return applied, &PlanStaleError{
				Rel: operation.Rel, Direction: operation.Direction, Resource: "post_write_cpuset.mems",
				Current: current.Mems, Target: operation.Target.Mems,
				Err: fmt.Errorf("post-write mems observation differs from target: current_parse=%v target_parse=%v",
					currentMemsErr, targetMemsErr),
			}
		}
	}
	return applied, nil
}

func (w safeCPUSetWriter) readAppliedObservation(ctx context.Context, operation PlanOperation) (AppliedPlanOperation, error) {
	current, err := w.driver.ReadEntry(ctx, operation.Rel)
	if err != nil {
		return AppliedPlanOperation{}, w.classifyHierarchyReadError(err, operation)
	}
	if current.Identity != operation.ExpectedIdentity {
		return AppliedPlanOperation{}, fmt.Errorf("%w: post-write rel=%q expected=%v current=%v",
			ErrCgroupIdentityChanged, operation.Rel, operation.ExpectedIdentity, current.Identity)
	}
	applied := AppliedPlanOperation{
		PlanID:    operation.PlanID,
		Rel:       operation.Rel,
		Direction: operation.Direction,
		Target:    operation.Target,
		Observed: CPUSetTarget{
			CPUs: observedCPUsForTargetProof(current, operation.Target.CPUs, w.driver.Capabilities()),
			Mems: current.Mems,
		},
	}
	return applied, nil
}

func (w safeCPUSetWriter) classifyHierarchyReadError(err error, operation PlanOperation) error {
	if w.driver.Classify(err, HierarchyOperationRead) != HierarchyErrorStale {
		return err
	}
	return &PlanStaleError{
		Rel:       operation.Rel,
		Direction: operation.Direction,
		Resource:  "hierarchy",
		Current:   operation.ExpectedCurrent.CPUs.String(),
		Target:    operation.Target.CPUs.String(),
		Err:       err,
	}
}

func validateLiveOperationDirection(
	operation PlanOperation,
	current EntryState,
	capabilities HierarchyCapabilities,
) error {
	stale := func(resource, currentValue, targetValue string, err error) error {
		return &PlanStaleError{
			Rel: operation.Rel, Direction: operation.Direction, Resource: resource,
			Current: currentValue, Target: targetValue, Err: err,
		}
	}
	_, cpuChanged, _ := classifySetDirection(
		operation.ExpectedCurrent.CPUs, operation.Target.CPUs,
	)
	if !cpuChanged {
		plannedDirection, _, directionErr := combinedWriteDirection(
			operation.Rel,
			operation.ExpectedCurrent,
			operation.Target,
			operation.OwnsMems,
			false,
		)
		if directionErr != nil || plannedDirection != operation.Direction {
			if directionErr == nil {
				directionErr = fmt.Errorf("declared=%s calculated=%s", operation.Direction, plannedDirection)
			}
			return stale("write_direction", string(operation.Direction), string(plannedDirection),
				fmt.Errorf("operation direction disagrees with CPU/mems transition: %w", directionErr))
		}
	}
	currentCPUs := observedCPUsForTargetProof(current, operation.Target.CPUs, capabilities)
	memsDirectedClear := isConfiguredInheritanceClear(operation, capabilities) && operation.WriteMems
	if !memsDirectedClear {
		switch operation.Direction {
		case WriteGrow:
			if !operation.ExpectedCurrent.CPUs.IsSubsetOf(currentCPUs) ||
				!currentCPUs.IsSubsetOf(operation.Target.CPUs) {
				return stale("cpuset.cpus", currentCPUs.String(), operation.Target.CPUs.String(), nil)
			}
		case WriteShrink:
			if !operation.Target.CPUs.IsSubsetOf(currentCPUs) ||
				!currentCPUs.IsSubsetOf(operation.ExpectedCurrent.CPUs) {
				return stale("cpuset.cpus", currentCPUs.String(), operation.Target.CPUs.String(), nil)
			}
		default:
			return fmt.Errorf("unsupported plan operation direction %q", operation.Direction)
		}
	}

	if !operation.OwnsMems {
		return nil
	}
	currentMems, currentErr := machine.Parse(current.Mems)
	expectedMems, expectedErr := machine.Parse(operation.ExpectedCurrent.Mems)
	targetMems, targetErr := machine.Parse(operation.Target.Mems)
	if currentErr != nil || expectedErr != nil || targetErr != nil {
		return stale("cpuset.mems", current.Mems, operation.Target.Mems,
			fmt.Errorf("parse live/expected/target mems: current=%v expected=%v target=%v",
				currentErr, expectedErr, targetErr))
	}
	if expectedMems.Equals(targetMems) {
		if !currentMems.Equals(targetMems) {
			return stale("cpuset.mems", current.Mems, operation.Target.Mems, nil)
		}
		return nil
	}
	// Live mems must advance monotonically in the planner's final merged direction, never infer it independently.
	switch operation.Direction {
	case WriteGrow:
		if !expectedMems.IsSubsetOf(currentMems) || !currentMems.IsSubsetOf(targetMems) {
			return stale("cpuset.mems", current.Mems, operation.Target.Mems, nil)
		}
	case WriteShrink:
		if !targetMems.IsSubsetOf(currentMems) || !currentMems.IsSubsetOf(expectedMems) {
			return stale("cpuset.mems", current.Mems, operation.Target.Mems, nil)
		}
	default:
		return fmt.Errorf("unsupported plan operation direction %q", operation.Direction)
	}
	return nil
}
