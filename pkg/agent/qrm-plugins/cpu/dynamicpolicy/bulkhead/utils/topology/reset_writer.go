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

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const maxResetEnforceDepth = 8

type resetCoordinatorWriter struct {
	driver           HierarchyDriver
	budget           *BudgetTracker
	adjustmentBudget *AdjustmentBudget
	reservation      *AdjustmentWriteReservation
	defaultMems      string
	res              *ConvergenceResult
}

type resetWrite struct {
	rel       string
	parentRel string
	current   machine.CPUSet
	target    machine.CPUSet
	mems      string
	expected  bool
	direction WriteDirection
}

func newResetCoordinatorWriter(driver HierarchyDriver, budget *BudgetTracker, defaultMems string, res *ConvergenceResult) resetCoordinatorWriter {
	return resetCoordinatorWriter{driver: driver, budget: budget, defaultMems: defaultMems, res: res}
}

func (w resetCoordinatorWriter) withAdjustmentBudget(budget *AdjustmentBudget) resetCoordinatorWriter {
	w.adjustmentBudget = budget
	return w
}

func (w resetCoordinatorWriter) execute(
	ctx context.Context,
	dag *TopoDAG,
	targets map[string]machine.CPUSet,
	allowEmptyTarget bool,
	expected map[string]machine.CPUSet,
	boundarySets ...map[string]struct{},
) error {
	if w.driver == nil {
		return fmt.Errorf("reset writer requires hierarchy driver")
	}
	if w.budget == nil {
		return fmt.Errorf("reset writer requires convergence budget")
	}
	controlled := map[string]*TopoNode{}
	for _, n := range dag.Nodes() {
		controlled[n.Rel] = n
	}
	var boundaries map[string]struct{}
	if len(boundarySets) > 0 {
		boundaries = boundarySets[0]
	}
	var writes []resetWrite
	var planErr error
	_ = dag.ForEachExpand(func(n *TopoNode) error {
		target := targets[n.Rel]
		if target.IsEmpty() && !allowEmptyTarget {
			if w.res != nil {
				w.res.Skipped++
			}
			return nil
		}
		parentRel := ""
		if parent := parentNodeOf(n); parent != nil {
			parentRel = parent.Rel
		}
		nodeWrite := resetWrite{
			rel:       n.Rel,
			parentRel: parentRel,
			target:    target.Clone(),
			mems:      memsForNode(n, w.defaultMems),
			expected:  true,
		}
		writes = append(writes, nodeWrite)
		w.collectPropagatedResetWrites(
			ctx, n.Rel, target, allowEmptyTarget, controlled, expected, boundaries, &writes, &planErr, 0)
		return nil
	})
	if planErr != nil {
		return planErr
	}
	writes, err := w.orderResetWrites(ctx, writes)
	if err != nil {
		return err
	}

	var reservationCost ExecutionReservationCost
	for _, write := range writes {
		writeCost := PhysicalWriteCost{CPUSetWrites: 1}
		if write.mems != "" {
			writeCost.MemsWrites = 1
		}
		reservationCost.Forward = addPhysicalWriteCost(reservationCost.Forward, writeCost)
	}
	reservation, err := w.adjustmentBudget.ReserveExecution(reservationCost)
	if err != nil {
		return err
	}
	w.reservation = reservation
	forwardStart := 0
	if w.res != nil {
		forwardStart = w.res.forwardWriteAttempts
	}
	defer func() {
		if w.res == nil {
			_ = reservation.settleTotals(0, 0)
			return
		}
		_ = reservation.settleTotals(w.res.forwardWriteAttempts-forwardStart, 0)
	}()

	var firstErr error
	skippedDynamicRoots := make(map[string]struct{})
	for _, write := range writes {
		if withinTraversalBoundary(write.rel, skippedDynamicRoots) {
			continue
		}
		localErr := w.writeResetRel(ctx, write.rel, write.parentRel, write.target, write.mems)
		if localErr != nil && w.shouldSkipDynamicUnavailableController(localErr, write.expected) {
			w.markDynamicResetSkip()
			skippedDynamicRoots[write.rel] = struct{}{}
			continue
		}
		if firstErr == nil && localErr != nil {
			firstErr = localErr
		}
	}
	return firstErr
}

func (w resetCoordinatorWriter) orderResetWrites(
	ctx context.Context,
	writes []resetWrite,
) ([]resetWrite, error) {
	byRel := make(map[string]int, len(writes))
	for i := range writes {
		driver, err := newReservedBudgetedHierarchyDriver(ctx, w.driver, w.budget, 1)
		if err != nil {
			return nil, err
		}
		current, err := driver.ReadEntry(ctx, writes[i].rel)
		if err != nil {
			if w.shouldSkipDynamicUnavailableController(err, writes[i].expected) {
				writes[i].direction = WritePublish
			} else {
				if w.res != nil {
					w.res.Attempted++
					w.res.Failed++
				}
				return nil, w.classifyReadError(err, writes[i].rel, CgroupIdentity{})
			}
		} else {
			writes[i].current = current.CPUs.Clone()
			direction, changed, monotonic := classifySetDirection(current.CPUs, writes[i].target)
			switch {
			case !changed:
				writes[i].direction = WritePublish
			case monotonic:
				writes[i].direction = direction
			default:
				// A replacement is deterministic but has no single monotonic
				// direction. Parent/child safety edges below still take
				// precedence over this neutral ordering class.
				writes[i].direction = WritePublish
			}
		}
		byRel[writes[i].rel] = i
	}

	edges := make([][]int, len(writes))
	indegree := make([]int, len(writes))
	addEdge := func(from, to int) {
		for _, existing := range edges[from] {
			if existing == to {
				return
			}
		}
		edges[from] = append(edges[from], to)
		indegree[to]++
	}
	for childIndex := range writes {
		parentIndex, ok := byRel[writes[childIndex].parentRel]
		if !ok {
			continue
		}
		parent, child := writes[parentIndex], writes[childIndex]
		childBeforeParent := parent.direction == WriteShrink && child.direction == WriteShrink ||
			(!child.current.IsEmpty() && !child.current.IsSubsetOf(parent.target))
		parentBeforeChild := parent.direction == WriteGrow && child.direction == WriteGrow ||
			(!child.target.IsEmpty() && !child.target.IsSubsetOf(parent.current))
		switch {
		case childBeforeParent && parentBeforeChild:
			return nil, fmt.Errorf(
				"reset relation %q -> %q requires incompatible write ordering: parent current=%s target=%s child current=%s target=%s",
				parent.rel, child.rel, parent.current.String(), parent.target.String(),
				child.current.String(), child.target.String())
		case childBeforeParent:
			addEdge(childIndex, parentIndex)
		case parentBeforeChild:
			addEdge(parentIndex, childIndex)
		}
	}

	less := func(left, right int) bool {
		leftRank := resetDirectionRank(writes[left].direction)
		rightRank := resetDirectionRank(writes[right].direction)
		if leftRank != rightRank {
			return leftRank < rightRank
		}
		leftDepth, rightDepth := childDepth(writes[left].rel), childDepth(writes[right].rel)
		if writes[left].direction == WriteShrink && leftDepth != rightDepth {
			return leftDepth > rightDepth
		}
		if writes[left].direction == WriteGrow && leftDepth != rightDepth {
			return leftDepth < rightDepth
		}
		return writes[left].rel < writes[right].rel
	}
	ready := make([]int, 0, len(writes))
	for i := range writes {
		if indegree[i] == 0 {
			ready = append(ready, i)
		}
	}
	ordered := make([]resetWrite, 0, len(writes))
	for len(ready) > 0 {
		sort.Slice(ready, func(i, j int) bool { return less(ready[i], ready[j]) })
		next := ready[0]
		ready = ready[1:]
		ordered = append(ordered, writes[next])
		for _, dependent := range edges[next] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
			}
		}
	}
	if len(ordered) != len(writes) {
		return nil, fmt.Errorf("reset write ordering contains incompatible parent/child dependencies")
	}
	return ordered, nil
}

func resetDirectionRank(direction WriteDirection) int {
	switch direction {
	case WriteGrow:
		return 0
	case WritePublish:
		return 1
	case WriteShrink:
		return 2
	default:
		return 3
	}
}

func (w resetCoordinatorWriter) writeResetRel(ctx context.Context, rel, parentRel string, target machine.CPUSet, mems string) error {
	if w.res != nil {
		w.res.Attempted++
	}
	operations := 2 // ReadEntry(rel) + WriteCPUs(rel).
	if parentRel != "" {
		operations += 2 // ReadEntry(parentRel) before and after.
	}
	if mems != "" {
		operations++ // WriteMems(rel).
	}
	driver, err := newReservedBudgetedHierarchyDriver(ctx, w.driver, w.budget, operations)
	if err != nil {
		return err
	}
	current, err := driver.ReadEntry(ctx, rel)
	if err != nil {
		if w.res != nil {
			w.res.Failed++
		}
		return w.classifyReadError(err, rel, CgroupIdentity{})
	}
	if parentRel != "" {
		parentBefore, err := driver.ReadEntry(ctx, parentRel)
		if err != nil {
			if w.res != nil {
				w.res.Failed++
			}
			return w.classifyReadError(err, parentRel, CgroupIdentity{})
		}
		parentAfter, err := driver.ReadEntry(ctx, parentRel)
		if err != nil {
			if w.res != nil {
				w.res.Failed++
			}
			return w.classifyReadError(err, parentRel, parentBefore.Identity)
		}
		if parentAfter.Identity != parentBefore.Identity {
			if w.res != nil {
				w.res.Failed++
			}
			return fmt.Errorf("%w: reset parent=%q expected=%v current=%v",
				ErrCgroupIdentityChanged, parentRel, parentBefore.Identity, parentAfter.Identity)
		}
	}
	if mems != "" {
		if err := w.reservation.RecordWriteAttempt(
			ctx, false, PhysicalWriteCost{MemsWrites: 1}); err != nil {
			return err
		}
		w.res.recordForwardWriteAttempt()
		if err := driver.WriteMems(ctx, rel, current.Identity, mems); err != nil {
			if w.res != nil {
				w.res.Failed++
			}
			return err
		}
	}
	if err := w.reservation.RecordWriteAttempt(
		ctx, false, PhysicalWriteCost{CPUSetWrites: 1}); err != nil {
		return err
	}
	w.res.recordForwardWriteAttempt()
	if err := driver.WriteCPUs(ctx, rel, current.Identity, target); err != nil {
		if w.res != nil {
			w.res.Failed++
		}
		return err
	}
	if w.res != nil {
		w.res.Applied++
	}
	return nil
}

func (w resetCoordinatorWriter) classifyReadError(err error, rel string, identity CgroupIdentity) error {
	return &SnapshotError{
		Operation: HierarchyOperationRead,
		Rel:       rel,
		Class:     w.driver.Classify(err, HierarchyOperationRead),
		Identity:  identity,
		Err:       err,
	}
}

func (w resetCoordinatorWriter) collectPropagatedResetWrites(
	ctx context.Context,
	parentRel string,
	parentTarget machine.CPUSet,
	allowEmptyTarget bool,
	controlled map[string]*TopoNode,
	expected map[string]machine.CPUSet,
	boundaries map[string]struct{},
	writes *[]resetWrite,
	firstErr *error,
	depth int,
) {
	if depth > maxResetEnforceDepth {
		if w.res != nil {
			w.res.Skipped++
		}
		return
	}
	driver, err := newReservedBudgetedHierarchyDriver(ctx, w.driver, w.budget, 1)
	if err != nil {
		if *firstErr == nil {
			*firstErr = err
		}
		return
	}
	children, err := driver.ListChildren(ctx, parentRel)
	if err != nil {
		if *firstErr == nil {
			*firstErr = err
		}
		return
	}
	for _, child := range children {
		childRel := filepath.Join(parentRel, child.Name)
		if withinTraversalBoundary(childRel, boundaries) {
			continue
		}
		if _, ok := controlled[childRel]; ok {
			continue
		}
		target, hasExpected := expected[childRel]
		if !hasExpected {
			// No canonical DesiredView/checkpoint ownership proof for this leaf
			// (external dedicated/shared/RNB Pod, pause, main container, or any
			// reclaim-domain dynamic descendant). The disabled reset may write only
			// controlled DAG nodes and proven leaves: never stamp the parent (machine)
			// target onto an unproven leaf. Skip the write but keep descending so a
			// deeper proven leaf still converges to its own canonical target.
			//
			// cgroup-v2 containment safety: when the controlled ancestor is being
			// emptied (allowEmptyTarget) and this unproven leaf still carries a
			// non-empty cpuset, we cannot empty the parent without stranding or
			// clobbering a leaf we have no proof for. Fail the whole reset during
			// planning, before any write, instead of leaving an inconsistent tree.
			//
			// The read itself must be trusted fail-closed too: only a genuinely
			// absent leaf (its cgroup dir raced away) or a subtree whose cpuset
			// controller is not attached has no cpuset that emptying the parent
			// could strand or clamp. Any other read failure leaves the leaf's
			// cpuset unproven, so failing open here would let an empty-parent
			// write silently clamp it; abort planning instead.
			if allowEmptyTarget && parentTarget.IsEmpty() {
				entry, readErr := driver.ReadEntry(ctx, childRel)
				switch {
				case readErr == nil && !entry.CPUs.IsEmpty():
					if *firstErr == nil {
						*firstErr = fmt.Errorf(
							"disabled reset would empty controlled ancestor %q but unproven descendant %q still holds non-empty cpuset %s: failing closed before any write",
							parentRel, childRel, entry.CPUs.String())
					}
					return
				case readErr != nil && !isCgroupPathAbsent(readErr) && !errors.Is(readErr, ErrCgroupControllerUnavailable):
					if *firstErr == nil {
						*firstErr = fmt.Errorf(
							"disabled reset would empty controlled ancestor %q but unproven descendant %q cpuset is unreadable: %w: failing closed before any write",
							parentRel, childRel, readErr)
					}
					return
				}
			}
			w.collectPropagatedResetWrites(
				ctx, childRel, parentTarget, allowEmptyTarget, controlled, expected, boundaries, writes, firstErr, depth+1)
			continue
		}
		mems := ""
		if parentNode := controlled[parentRel]; parentNode != nil && parentNode.Role == TopoNodeRoleReclaimNUMABucket {
			mems = memsForNode(parentNode, w.defaultMems)
		}
		*writes = append(*writes, resetWrite{
			rel: childRel, parentRel: parentRel, target: target.Clone(), mems: mems, expected: hasExpected,
		})
		w.collectPropagatedResetWrites(
			ctx, childRel, target, allowEmptyTarget, controlled, expected, boundaries, writes, firstErr, depth+1)
	}
}

func withinTraversalBoundary(rel string, boundaries map[string]struct{}) bool {
	rel = filepath.Clean(rel)
	for boundary := range boundaries {
		boundary = filepath.Clean(boundary)
		if rel == boundary || (boundary != "." && len(rel) > len(boundary) &&
			rel[:len(boundary)] == boundary && rel[len(boundary)] == filepath.Separator) {
			return true
		}
	}
	return false
}

func (w resetCoordinatorWriter) shouldSkipDynamicUnavailableController(err error, hasExpected bool) bool {
	return !hasExpected && errors.Is(err, ErrCgroupControllerUnavailable)
}

func (w resetCoordinatorWriter) markDynamicResetSkip() {
	if w.res == nil {
		return
	}
	w.res.Skipped++
	if w.res.Failed > 0 {
		w.res.Failed--
	}
}
