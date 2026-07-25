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
	"syscall"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const maxSafeCPUSetWriteAttempts = 3

type safeCPSetWriter struct {
	ctx         context.Context
	cg          cgroupclient.CgroupClient
	defaultMems string
	res         *DAGApplyResult
}

func newSafeCPUSetWriter(ctx context.Context, cg cgroupclient.CgroupClient, defaultMems string, res *DAGApplyResult) safeCPSetWriter {
	return safeCPSetWriter{
		ctx:         ctx,
		cg:          cg,
		defaultMems: defaultMems,
		res:         res,
	}
}

func (w safeCPSetWriter) growNodeWithParentBridge(node *TopoNode, target machine.CPUSet) error {
	if node == nil {
		return nil
	}
	if err := w.ensureParentContains(node, target); err != nil {
		return err
	}
	return w.writeNode(node, target)
}

// cgroup v1 rejects a parent shrink while any live descendant still owns CPUs
// outside the new parent target. First preserve a parent bridge for the
// observed child union, then shrink descendants, and only then shrink the
// parent to its final target.
func (w safeCPSetWriter) shrinkParentWithLiveChildUnion(node *TopoNode, target machine.CPUSet) error {
	if node == nil {
		return nil
	}
	liveChildUnion, err := w.liveChildUnion(node.Rel)
	if err != nil {
		return err
	}
	// The bridge is a temporary superset for the observed children, not the
	// desired steady-state parent target.
	bridgeTarget := target.Union(liveChildUnion)
	parentActual, err := w.cg.ReadCPUSet(w.ctx, node.Rel)
	if err != nil {
		return fmt.Errorf("read parent cpuset before parent shrink, rel=%s target=%s: %w", node.Rel, target.String(), err)
	}
	if !bridgeTarget.Equals(parentActual) {
		if err := w.writeBridgeNode(node, target, bridgeTarget); err != nil {
			return err
		}
	}
	if err := w.shrinkLiveChildrenToParent(node.Rel, target, 0); err != nil {
		return err
	}
	refreshedChildUnion, err := w.liveChildUnion(node.Rel)
	if err != nil {
		return err
	}
	if !refreshedChildUnion.IsSubsetOf(target) {
		return fmt.Errorf("children_not_ready: parent=%s target=%s liveChildUnion=%s",
			node.Rel, target.String(), refreshedChildUnion.String())
	}
	current, err := w.cg.ReadCPUSet(w.ctx, node.Rel)
	if err != nil {
		return fmt.Errorf("read parent cpuset before final parent shrink, rel=%s target=%s: %w", node.Rel, target.String(), err)
	}
	if current.Equals(target) {
		return nil
	}
	return w.writeNode(node, target)
}

func (w safeCPSetWriter) ensureParentContains(node *TopoNode, childTarget machine.CPUSet) error {
	parent := parentNodeOf(node)
	if parent == nil || childTarget.IsEmpty() {
		return nil
	}
	parentActual, err := w.cg.ReadCPUSet(w.ctx, parent.Rel)
	if err != nil {
		return fmt.Errorf("read parent cpuset before child grow, parent=%s child=%s target=%s: %w",
			parent.Rel, node.Rel, childTarget.String(), err)
	}
	if childTarget.IsSubsetOf(parentActual) {
		return nil
	}
	parentBridge := parentActual.Union(childTarget)
	if err := w.ensureParentContains(parent, parentBridge); err != nil {
		return err
	}
	if parentBridge.Equals(parentActual) {
		return nil
	}
	return w.writeNode(parent, parentBridge)
}

func (w safeCPSetWriter) liveChildUnion(parentRel string) (machine.CPUSet, error) {
	children, err := w.cg.ListChildren(w.ctx, parentRel)
	if err != nil {
		return machine.NewCPUSet(), fmt.Errorf("list live children, parent=%s: %w", parentRel, err)
	}
	union := machine.NewCPUSet()
	for _, child := range children {
		childRel := filepath.Join(parentRel, child)
		current, err := w.cg.ReadCPUSet(w.ctx, childRel)
		if err != nil {
			if isCgroupNotFoundError(err) {
				continue
			}
			return union, fmt.Errorf("read live child cpuset, child=%s: %w", childRel, err)
		}
		union = union.Union(current)
	}
	return union, nil
}

// Dynamic container descendants are outside the static DAG, but they must be
// recursively narrowed before their controlled parent can shrink. Children
// that disappear during traversal follow the existing not-found path.
func (w safeCPSetWriter) shrinkLiveChildrenToParent(parentRel string, parentTarget machine.CPUSet, depth int) error {
	if depth >= maxEnforceDepth {
		return fmt.Errorf("%w: rel=%s depth=%d target=%s", errDepthLimitReached, parentRel, depth, parentTarget.String())
	}
	children, err := w.cg.ListChildren(w.ctx, parentRel)
	if err != nil {
		return fmt.Errorf("list live children for shrink, parent=%s: %w", parentRel, err)
	}
	for _, child := range children {
		childRel := filepath.Join(parentRel, child)
		current, err := w.cg.ReadCPUSet(w.ctx, childRel)
		if err != nil {
			if isCgroupNotFoundError(err) {
				continue
			}
			return fmt.Errorf("read live child for shrink, child=%s: %w", childRel, err)
		}
		if current.IsEmpty() || current.IsSubsetOf(parentTarget) {
			if err := w.shrinkLiveChildrenToParent(childRel, current, depth+1); err != nil {
				return err
			}
			continue
		}
		childTarget := current.Intersection(parentTarget)
		if childTarget.IsEmpty() && !parentTarget.IsEmpty() {
			childTarget = parentTarget
		}
		if err := w.shrinkDynamicRelToParent(childRel, childTarget, depth+1); err != nil {
			return err
		}
	}
	return nil
}

func (w safeCPSetWriter) shrinkDynamicRelToParent(rel string, target machine.CPUSet, depth int) error {
	if depth >= maxEnforceDepth {
		return fmt.Errorf("%w: rel=%s depth=%d target=%s", errDepthLimitReached, rel, depth, target.String())
	}
	if err := w.shrinkLiveChildrenToParent(rel, target, depth+1); err != nil {
		return err
	}
	if w.res != nil {
		w.res.Attempted++
	}
	if err := applyCPUSet(w.ctx, w.cg, rel, target, ""); err != nil {
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

func (w safeCPSetWriter) writeNode(node *TopoNode, target machine.CPUSet) error {
	if node == nil {
		return nil
	}
	// EBUSY means the kernel observed a hierarchy that no longer matches the
	// last snapshot. Retry only EBUSY, reconcile live children before the next
	// attempt, and keep retries bounded so persistent ownership errors remain
	// visible to the periodical retry path.
	for attempt := 0; attempt < maxSafeCPUSetWriteAttempts; attempt++ {
		if w.res != nil {
			w.res.Attempted++
		}
		err := applyCPUSet(w.ctx, w.cg, node.Rel, target, memsForNode(node, w.defaultMems))
		if err == nil {
			if w.res != nil {
				w.res.Applied++
			}
			return nil
		}

		if w.res != nil {
			w.res.Failed++
		}
		if !isCgroupBusyError(err) || attempt == maxSafeCPUSetWriteAttempts-1 {
			return err
		}
		if err := w.reconcileLiveChildrenBeforeRetry(node.Rel, target); err != nil {
			return err
		}
	}
	return nil
}

func (w safeCPSetWriter) writeBridgeNode(node *TopoNode, finalTarget, bridgeTarget machine.CPUSet) error {
	if node == nil {
		return nil
	}
	target := bridgeTarget
	// EBUSY means the kernel observed a hierarchy that no longer matches the
	// last snapshot. Retry only EBUSY and recalculate the temporary bridge from
	// a fresh live-child union before the next attempt; bounded retries retain
	// persistent ownership errors for the periodical retry path.
	for attempt := 0; attempt < maxSafeCPUSetWriteAttempts; attempt++ {
		if w.res != nil {
			w.res.Attempted++
		}
		err := applyCPUSet(w.ctx, w.cg, node.Rel, target, memsForNode(node, w.defaultMems))
		if err == nil {
			if w.res != nil {
				w.res.Applied++
			}
			return nil
		}
		if w.res != nil {
			w.res.Failed++
		}
		if !isCgroupBusyError(err) || attempt == maxSafeCPUSetWriteAttempts-1 {
			return err
		}

		liveChildUnion, err := w.liveChildUnion(node.Rel)
		if err != nil {
			return err
		}
		target = finalTarget.Union(liveChildUnion)
	}
	return nil
}

func (w safeCPSetWriter) reconcileLiveChildrenBeforeRetry(parentRel string, parentTarget machine.CPUSet) error {
	liveChildUnion, err := w.liveChildUnion(parentRel)
	if err != nil {
		return err
	}
	if liveChildUnion.IsSubsetOf(parentTarget) {
		return nil
	}
	return w.shrinkLiveChildrenToParent(parentRel, parentTarget, 0)
}

func isCgroupBusyError(err error) bool {
	return errors.Is(err, syscall.EBUSY)
}
