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
	"strconv"
	"strings"
	"syscall"

	"k8s.io/klog/v2"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	maxSafeCPUSetWriteAttempts = 3
	maxCPUSetFailureDumpNodes  = 64
	maxCPUSetFailureDumpDepth  = 3
	maxCgroupFileLogFields     = 16
	maxLiveChildShrinkAttempts = 3
	// maxReclaimBucketShrinkAttempts bounds how many times the reclaim-bucket
	// shrink re-runs descendant normalization + the live-child recheck within a
	// single pass. Under high pod churn a reclaim child can materialize (or
	// re-inherit the bridged parent cpuset) between the normalization scan and
	// the recheck; a few extra rounds narrow such a freshly born straggler in
	// the same pass instead of tripping children_not_ready on first sight. When
	// the straggler still holds a previous generation after these attempts, the
	// caller falls back to the deferred-convergence model.
	maxReclaimBucketShrinkAttempts = 3
)

// errDeferConvergence marks a generational parent shrink that could not narrow
// to its final target within the bounded retries this pass (a child reclaim
// bucket still holds a near-disjoint previous generation), yet the parent was
// kept as a valid cgroup v1 superset of every live child. It is non-fatal: the
// next periodical reconcile re-runs the same shrink path and converges once the
// advisor has moved the child bucket to the new segment. Callers must never use
// it to mask a genuine parent-below-child illegal state.
var errDeferConvergence = errors.New("bulkhead cpuset convergence deferred to next reconcile")

// IsDeferConvergenceError reports whether err (or any wrapped error) is the
// deferred-convergence sentinel. It is exported so the admission path can
// distinguish a transient topology lag from a real allocation failure.
func IsDeferConvergenceError(err error) bool {
	return errors.Is(err, errDeferConvergence)
}

type safeCPSetWriter struct {
	ctx             context.Context
	cg              cgroupclient.CgroupClient
	defaultMems     string
	res             *DAGApplyResult
	controlledByRel map[string]*TopoNode
	targetByRel     map[string]machine.CPUSet
	cpuDetails      machine.CPUDetails
	dynamicPolicy   dynamicDescendantPolicy
	constrainBridge bool
}

type dynamicDescendantPolicy struct {
	defaultMems     string
	controlledByRel map[string]*TopoNode
	targetByRel     map[string]machine.CPUSet
}

func newSafeCPUSetWriter(ctx context.Context, cg cgroupclient.CgroupClient, defaultMems string, res *DAGApplyResult) safeCPSetWriter {
	writer := safeCPSetWriter{
		ctx:             ctx,
		cg:              cg,
		defaultMems:     defaultMems,
		res:             res,
		controlledByRel: map[string]*TopoNode{},
		targetByRel:     map[string]machine.CPUSet{},
	}
	writer.dynamicPolicy = newDynamicDescendantPolicy(writer.defaultMems, writer.controlledByRel, writer.targetByRel)
	return writer
}

func newSafeCPUSetWriterForDAG(ctx context.Context, cg cgroupclient.CgroupClient, dag *TopoDAG, targets map[string]machine.CPUSet, defaultMems string, res *DAGApplyResult) safeCPSetWriter {
	writer := newSafeCPUSetWriter(ctx, cg, defaultMems, res)
	if dag == nil {
		return writer
	}
	for _, node := range dag.Nodes() {
		writer.controlledByRel[node.Rel] = node
		if target, ok := targets[node.Rel]; ok {
			writer.targetByRel[node.Rel] = target
		} else {
			writer.targetByRel[node.Rel] = node.CPUs
		}
	}
	writer.dynamicPolicy = newDynamicDescendantPolicy(writer.defaultMems, writer.controlledByRel, writer.targetByRel)
	return writer
}

func (w safeCPSetWriter) withConstrainBridgeGrowth(constrain bool) safeCPSetWriter {
	w.constrainBridge = constrain
	return w
}

func (w safeCPSetWriter) withTargetByRel(targets map[string]machine.CPUSet) safeCPSetWriter {
	w.targetByRel = cloneCPUSetMap(targets)
	w.dynamicPolicy = newDynamicDescendantPolicy(w.defaultMems, w.controlledByRel, w.targetByRel)
	return w
}

// withCPUDetails supplies NUMA topology so live-child shrink can refuse to pull
// an uncontrolled physical reclaim NUMA bucket across NUMA nodes. It is optional:
// callers without CPUDetails keep the legacy behavior.
func (w safeCPSetWriter) withCPUDetails(cpuDetails machine.CPUDetails) safeCPSetWriter {
	w.cpuDetails = cpuDetails
	return w
}

func newDynamicDescendantPolicy(defaultMems string, controlledByRel map[string]*TopoNode, targetByRel map[string]machine.CPUSet) dynamicDescendantPolicy {
	return dynamicDescendantPolicy{
		defaultMems:     defaultMems,
		controlledByRel: controlledByRel,
		targetByRel:     targetByRel,
	}
}

func (p dynamicDescendantPolicy) Resolve(rel string, requested machine.CPUSet) (machine.CPUSet, string, bool) {
	bucket := p.findAncestorReclaimBucket(rel)
	if bucket == nil || rel == bucket.Rel {
		return requested, "", false
	}
	bucketTarget, ok := p.targetByRel[bucket.Rel]
	if !ok {
		bucketTarget = bucket.CPUs
	}
	desired := requested.Intersection(bucketTarget)
	if desired.IsEmpty() {
		desired = bucketTarget
	}
	return desired, memsForNode(bucket, p.defaultMems), true
}

func (p dynamicDescendantPolicy) findAncestorReclaimBucket(rel string) *TopoNode {
	var best *TopoNode
	for bucketRel, node := range p.controlledByRel {
		if node == nil || node.Role != TopoNodeRoleReclaimNUMABucket {
			continue
		}
		if rel == bucketRel || strings.HasPrefix(rel, bucketRel+"/") {
			if best == nil || len(bucketRel) > len(best.Rel) {
				best = node
			}
		}
	}
	return best
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
	if node.Role == TopoNodeRoleReclaimNUMABucket {
		return w.shrinkReclaimBucketWithDescendants(node, target)
	}
	if err := w.shrinkControlledChildrenToTargets(node.Rel); err != nil {
		return err
	}
	controlledChildUnion, err := w.controlledChildUnion(node.Rel)
	if err != nil {
		return err
	}
	if !controlledChildUnion.IsSubsetOf(target) {
		parentActual, readErr := w.cg.ReadCPUSet(w.ctx, node.Rel)
		if readErr != nil {
			return fmt.Errorf("read parent cpuset before controlled-child bridge, rel=%s target=%s: %w", node.Rel, target.String(), readErr)
		}
		bridgeTarget := parentActual.Union(controlledChildUnion)
		if !parentActual.Equals(bridgeTarget) {
			if err := w.writeBridgeNode(node, target, bridgeTarget); err != nil {
				return w.deferSafeParentBusy(node, err, "controlled_child_bridge")
			}
		}
		general.Infof("topo_dag_writer: keep_parent_bridge_for_controlled_children rel=%q target=%s controlledChildUnion=%s bridgeTarget=%s",
			node.Rel, target.String(), controlledChildUnion.String(), bridgeTarget.String())
		return nil
	}
	liveChildUnion, err := w.liveChildUnion(node.Rel)
	if err != nil {
		return err
	}
	// The bridge is a temporary superset for the observed children, not the
	// desired steady-state parent target.
	parentActual, err := w.cg.ReadCPUSet(w.ctx, node.Rel)
	if err != nil {
		return fmt.Errorf("read parent cpuset before parent shrink, rel=%s target=%s: %w", node.Rel, target.String(), err)
	}
	bridgeTarget := target.Union(liveChildUnion)
	if w.constrainBridge {
		bridgeTarget = parentActual.Union(liveChildUnion)
	}
	if !bridgeTarget.Equals(parentActual) {
		if err := w.writeBridgeNode(node, target, bridgeTarget); err != nil {
			return w.deferSafeParentBusy(node, err, "live_child_bridge")
		}
	}
	var parked machine.CPUSet
	var effectiveTarget machine.CPUSet
	var refreshedChildUnion machine.CPUSet
	converged := false
	for attempt := 0; attempt < maxLiveChildShrinkAttempts; attempt++ {
		parked, err = w.shrinkLiveChildrenToParent(node.Rel, target, 0)
		if err != nil {
			return err
		}
		// A parked cross-NUMA reclaim bucket keeps CPUs outside the desired
		// target. The parent must retain them (a valid cgroup v1 superset) until
		// a later advisor round drains the bucket through its own controlled
		// transition.
		effectiveTarget = target.Union(parked)
		refreshedChildUnion, err = w.liveChildUnion(node.Rel)
		if err != nil {
			return err
		}
		if refreshedChildUnion.IsSubsetOf(effectiveTarget) {
			converged = true
			break
		}
	}
	if !converged {
		current, readErr := w.cg.ReadCPUSet(w.ctx, node.Rel)
		if readErr != nil {
			return fmt.Errorf("read parent cpuset before defer bridge, rel=%s target=%s: %w", node.Rel, target.String(), readErr)
		}
		deferBridgeTarget := effectiveTarget.Union(refreshedChildUnion)
		if w.constrainBridge {
			deferBridgeTarget = current.Union(refreshedChildUnion)
		}
		if !current.Equals(deferBridgeTarget) {
			if err := w.writeBridgeNode(node, effectiveTarget, deferBridgeTarget); err != nil {
				return w.deferSafeParentBusy(node, err, "defer_bridge")
			}
		}
		if w.parentSupersetHeld(node) {
			general.Warningf("topo_dag_writer: defer_convergence_live_children rel=%q target=%s parked=%s liveChildUnion=%s",
				node.Rel, target.String(), parked.String(), refreshedChildUnion.String())
			return errDeferConvergence
		}
		return fmt.Errorf("children_not_ready: parent=%s target=%s parked=%s liveChildUnion=%s",
			node.Rel, target.String(), parked.String(), refreshedChildUnion.String())
	}
	current, err := w.cg.ReadCPUSet(w.ctx, node.Rel)
	if err != nil {
		return fmt.Errorf("read parent cpuset before final parent shrink, rel=%s target=%s: %w", node.Rel, target.String(), err)
	}
	if current.Equals(effectiveTarget) {
		return nil
	}
	// Defer instead of failing hard when only the final narrowing cannot land:
	// the parent already covers every live child (checked above), so a persistent
	// EBUSY here means a child bucket still holds a previous generation that a
	// later reconcile will drain.
	return w.finalParentShrink(node, effectiveTarget)
}

func (w safeCPSetWriter) deferSafeParentBusy(node *TopoNode, err error, stage string) error {
	if !isCgroupBusyError(err) || !w.parentSupersetHeld(node) {
		return err
	}
	general.Warningf("topo_dag_writer: defer_convergence_parent_busy stage=%q rel=%q err=%v",
		stage, node.Rel, err)
	return errDeferConvergence
}

func (w safeCPSetWriter) shrinkControlledChildrenToTargets(parentRel string) error {
	children, err := w.cg.ListChildren(w.ctx, parentRel)
	if err != nil {
		return fmt.Errorf("list controlled children before parent shrink, parent=%s: %w", parentRel, err)
	}
	for _, child := range children {
		childRel := filepath.Join(parentRel, child)
		childNode, controlled := w.controlledByRel[childRel]
		if !controlled {
			continue
		}
		target, ok := w.targetByRel[childRel]
		if !ok {
			target = childNode.CPUs
		}
		current, err := w.cg.ReadCPUSet(w.ctx, childRel)
		if err != nil {
			if isCgroupNotFoundError(err) {
				continue
			}
			return fmt.Errorf("read controlled child before parent shrink, child=%s: %w", childRel, err)
		}
		if current.IsSubsetOf(target) {
			continue
		}
		if err := w.shrinkParentWithLiveChildUnion(childNode, target); err != nil {
			return err
		}
	}
	return nil
}

func (w safeCPSetWriter) controlledChildUnion(parentRel string) (machine.CPUSet, error) {
	children, err := w.cg.ListChildren(w.ctx, parentRel)
	if err != nil {
		return machine.NewCPUSet(), fmt.Errorf("list controlled children, parent=%s: %w", parentRel, err)
	}
	union := machine.NewCPUSet()
	for _, child := range children {
		childRel := filepath.Join(parentRel, child)
		if _, controlled := w.controlledByRel[childRel]; !controlled {
			continue
		}
		current, err := w.cg.ReadCPUSet(w.ctx, childRel)
		if err != nil {
			if isCgroupNotFoundError(err) {
				continue
			}
			return union, fmt.Errorf("read controlled child cpuset, child=%s: %w", childRel, err)
		}
		union = union.Union(current)
	}
	return union, nil
}

func (w safeCPSetWriter) shrinkReclaimBucketWithDescendants(node *TopoNode, target machine.CPUSet) error {
	parentActual, err := w.cg.ReadCPUSet(w.ctx, node.Rel)
	if err != nil {
		return fmt.Errorf("read reclaim bucket before shrink, rel=%s target=%s: %w", node.Rel, target.String(), err)
	}
	hasChildren, err := w.hasChildren(node.Rel)
	if err != nil {
		return err
	}
	if hasChildren && !target.IsSubsetOf(parentActual) {
		bridgeTarget := parentActual.Union(target)
		// The bucket bridge may be a net expansion relative to the bucket's
		// current cpuset (e.g. a converge_shrink drain that first has to widen
		// the bucket to cover CPUs entering from a sibling NUMA node). cgroup v1
		// rejects writing a child cpuset that is not a subset of its parent, so
		// the real parent (e.g. kubesandbox) must be grown to a superset of the
		// bridge target first. growNodeWithParentBridge already relies on this
		// invariant via ensureParentContains; the reclaim-bucket bridge path had
		// omitted it, producing EACCES ("permission denied") when the parent did
		// not yet contain the bridge CPUs.
		if err := w.ensureParentContains(node, bridgeTarget); err != nil {
			return err
		}
		if err := w.writeBridgeNode(node, target, bridgeTarget); err != nil {
			return err
		}
	}

	bucketMems := memsForNode(node, w.defaultMems)
	// Re-run descendant normalization and the live-child recheck a bounded
	// number of times. Under high churn a reclaim child can be created (or
	// re-inherit the bridged parent cpuset that still carries the previous
	// generation) in the window between the normalization scan and the
	// recheck, so a single pass would trip children_not_ready on a straggler
	// that a second scan would have narrowed. Each retry re-normalizes any
	// child born since the last scan; the loop exits as soon as every live
	// child is a subset of the target.
	var refreshedChildUnion machine.CPUSet
	converged := false
	for attempt := 0; attempt < maxReclaimBucketShrinkAttempts; attempt++ {
		if err := w.normalizeReclaimBucketDescendants(node.Rel, target, bucketMems, 0); err != nil {
			return err
		}
		var err error
		refreshedChildUnion, err = w.liveChildUnion(node.Rel)
		if err != nil {
			return err
		}
		if refreshedChildUnion.IsSubsetOf(target) {
			converged = true
			break
		}
	}
	if !converged {
		// A live child still holds a near-disjoint previous generation after the
		// bounded retries. This is an uncontrolled reclaim descendant owned by a
		// separate advisor-controlled transition, so forcibly clamping it would
		// stomp a live workload. Provided the bucket is still a valid cgroup v1
		// superset of every live child (the bridge above guarantees this), defer
		// the final narrowing to the next reconcile instead of failing Pod
		// admission with a hard children_not_ready. Only when the parent is NOT a
		// superset (a genuine illegal parent-below-child state) is the hard error
		// surfaced.
		if w.parentSupersetHeld(node) {
			general.Warningf("topo_dag_writer: defer_convergence_reclaim_bucket rel=%q target=%s liveChildUnion=%s",
				node.Rel, target.String(), refreshedChildUnion.String())
			return errDeferConvergence
		}
		return fmt.Errorf("children_not_ready: parent=%s target=%s liveChildUnion=%s",
			node.Rel, target.String(), refreshedChildUnion.String())
	}
	current, err := w.cg.ReadCPUSet(w.ctx, node.Rel)
	if err != nil {
		return fmt.Errorf("read reclaim bucket before final shrink, rel=%s target=%s: %w", node.Rel, target.String(), err)
	}
	if current.Equals(target) {
		return nil
	}
	// The bucket already covers every live descendant (checked above); a
	// persistent EBUSY on the final narrowing is deferred to the next reconcile
	// rather than failing the caller.
	return w.finalParentShrink(node, target)
}

func (w safeCPSetWriter) hasChildren(rel string) (bool, error) {
	children, err := w.cg.ListChildren(w.ctx, rel)
	if err != nil {
		return false, fmt.Errorf("list children, parent=%s: %w", rel, err)
	}
	return len(children) > 0, nil
}

func (w safeCPSetWriter) normalizeReclaimBucketDescendants(parentRel string, bucketTarget machine.CPUSet, bucketMems string, depth int) error {
	if depth >= maxEnforceDepth {
		return fmt.Errorf("%w: rel=%s depth=%d target=%s", errDepthLimitReached, parentRel, depth, bucketTarget.String())
	}
	children, err := w.cg.ListChildren(w.ctx, parentRel)
	if err != nil {
		// A dynamic reclaim descendant can be removed by kubelet between the
		// parent's directory scan and this recursive enumeration. It has no
		// remaining children to normalize, so treat ENOENT exactly like the
		// disappearing-child ReadCPUSet path below rather than failing the
		// enclosing bucket shrink and Pod admission.
		if isCgroupNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("list reclaim bucket children, parent=%s: %w", parentRel, err)
	}
	for _, child := range children {
		childRel := filepath.Join(parentRel, child)
		if err := w.normalizeReclaimBucketDescendants(childRel, bucketTarget, bucketMems, depth+1); err != nil {
			return err
		}
		current, err := w.cg.ReadCPUSet(w.ctx, childRel)
		if err != nil {
			if isCgroupNotFoundError(err) {
				continue
			}
			return fmt.Errorf("read reclaim bucket child, child=%s: %w", childRel, err)
		}
		desired := current.Intersection(bucketTarget)
		if desired.IsEmpty() {
			desired = bucketTarget
		}
		// The reclaim bucket is bridged by shrinkReclaimBucketWithDescendants, but
		// the uncontrolled per-container parents between the bucket and this leaf
		// are not. During a NUMA drain the bucket target can shift a descendant
		// into a new NUMA range that is not yet a subset of its immediate parent
		// (e.g. leaf target 33-39,81-87 while the per-sandbox parent still holds
		// 29-31,73-79). cgroup v1 rejects writing a child cpuset outside its
		// parent, so widen the intermediate parents to a superset of the child
		// target before the leaf write; otherwise the leaf apply fails with EACCES
		// ("permission denied") and blocks the advisor loop.
		if err := w.ensureReclaimDescendantParentContains(childRel, desired, bucketMems); err != nil {
			return err
		}
		if err := w.writeDynamicRel(childRel, desired, bucketMems); err != nil {
			return err
		}
	}
	return nil
}

// ensureReclaimDescendantParentContains grows the uncontrolled per-container
// parents between a reclaim-bucket descendant and its bucket so that every
// intermediate parent is a superset of childTarget before the child is written.
// It walks upward until it reaches a parent that already contains the target or
// a controlled node (the reclaim bucket, which its own write path already
// bridged). The bridge write is a raw grow (the union of the parent's current
// cpuset and the child target) that deliberately bypasses the dynamic-descendant
// clamp in writeDynamicRel: the parent must temporarily hold both the outgoing
// and incoming NUMA ranges so the leaf can move without violating the cgroup v1
// parent-superset rule. The subsequent post-order write of that parent clamps it
// back down to its desired target once its own children are already inside it.
func (w safeCPSetWriter) ensureReclaimDescendantParentContains(childRel string, childTarget machine.CPUSet, bucketMems string) error {
	if childTarget.IsEmpty() {
		return nil
	}
	parentRel := filepath.Dir(childRel)
	if parentRel == "." || parentRel == childRel {
		return nil
	}
	if _, controlled := w.controlledByRel[parentRel]; controlled {
		return nil
	}
	parentActual, err := w.cg.ReadCPUSet(w.ctx, parentRel)
	if err != nil {
		if isCgroupNotFoundError(err) {
			return nil
		}
		return fmt.Errorf("read reclaim descendant parent before child grow, parent=%s child=%s target=%s: %w",
			parentRel, childRel, childTarget.String(), err)
	}
	if childTarget.IsSubsetOf(parentActual) {
		return nil
	}
	parentBridge := parentActual.Union(childTarget)
	if err := w.ensureReclaimDescendantParentContains(parentRel, parentBridge, bucketMems); err != nil {
		return err
	}
	if w.res != nil {
		w.res.Attempted++
	}
	if err := applyCPUSet(w.ctx, w.cg, parentRel, parentBridge, bucketMems); err != nil {
		if w.res != nil {
			w.res.Failed++
		}
		return fmt.Errorf("grow reclaim descendant parent bridge, parent=%s bridge=%s: %w",
			parentRel, parentBridge.String(), err)
	}
	if w.res != nil {
		w.res.Applied++
	}
	return nil
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
		if _, controlled := w.controlledByRel[childRel]; controlled {
			continue
		}
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
//
// The returned "parked" set is the union of CPUs owned by un-drainable
// cross-NUMA physical reclaim buckets that were deliberately left untouched
// (see below). Callers must keep these CPUs in the parent's effective target
// so the parent stays a valid cgroup v1 superset until a later advisor round
// drains the bucket through its own controlled transition.
func (w safeCPSetWriter) shrinkLiveChildrenToParent(parentRel string, parentTarget machine.CPUSet, depth int) (machine.CPUSet, error) {
	parked := machine.NewCPUSet()
	if depth >= maxEnforceDepth {
		return parked, fmt.Errorf("%w: rel=%s depth=%d target=%s", errDepthLimitReached, parentRel, depth, parentTarget.String())
	}
	children, err := w.cg.ListChildren(w.ctx, parentRel)
	if err != nil {
		return parked, fmt.Errorf("list live children for shrink, parent=%s: %w", parentRel, err)
	}
	for _, child := range children {
		childRel := filepath.Join(parentRel, child)
		if _, controlled := w.controlledByRel[childRel]; controlled {
			continue
		}
		current, err := w.cg.ReadCPUSet(w.ctx, childRel)
		if err != nil {
			if isCgroupNotFoundError(err) {
				continue
			}
			return parked, fmt.Errorf("read live child for shrink, child=%s: %w", childRel, err)
		}
		if current.IsEmpty() || current.IsSubsetOf(parentTarget) {
			childParked, err := w.shrinkLiveChildrenToParent(childRel, current, depth+1)
			if err != nil {
				return parked, err
			}
			parked = parked.Union(childParked)
			continue
		}
		// An uncontrolled child that is itself a physical reclaim NUMA bucket owns
		// CPUs pinned to its own NUMA node. If the parent's target has no CPU on
		// that NUMA node, clamping the bucket to the parent target would inject
		// foreign-NUMA CPUs and violate NUMA affinity. The bucket is outside the
		// current DAG, so no in-NUMA target can be computed here. Instead of
		// failing the whole apply (which fails Pod admission with
		// UnexpectedAdmissionError during a legitimate NUMA-drain transition),
		// park the bucket: leave its cpuset untouched and report its CPUs upward
		// so the parent keeps them as a temporary superset (valid under cgroup
		// v1). A later advisor round drains the bucket through its own controlled
		// transition once it enters the DAG. When the parent target still covers
		// the bucket's NUMA node (e.g. an empty stale bucket parked inside the
		// parent), the clamp path below remains valid.
		if bucketRel, numaID, ok := findPhysicalReclaimBucketRel(childRel); ok && bucketRel == childRel && len(w.cpuDetails) > 0 {
			if parentTarget.Intersection(w.cpuDetails.CPUsInNUMANodes(numaID)).IsEmpty() {
				general.Warningf("topo_dag_writer: park_uncontrolled_physical_reclaim_bucket_cross_numa parent=%s child=%s numa=%d childCPUs=%s parentTarget=%s",
					parentRel, childRel, numaID, current.String(), parentTarget.String())
				parked = parked.Union(current)
				continue
			}
		}
		childTarget := current.Intersection(parentTarget)
		if childTarget.IsEmpty() && !parentTarget.IsEmpty() {
			childTarget = parentTarget
		}
		childParked, err := w.shrinkDynamicRelToParent(childRel, childTarget, depth+1)
		if err != nil {
			return parked, err
		}
		parked = parked.Union(childParked)
	}
	return parked, nil
}

func (w safeCPSetWriter) shrinkDynamicRelToParent(rel string, target machine.CPUSet, depth int) (machine.CPUSet, error) {
	parked := machine.NewCPUSet()
	if _, controlled := w.controlledByRel[rel]; controlled {
		return parked, nil
	}
	if depth >= maxEnforceDepth {
		return parked, fmt.Errorf("%w: rel=%s depth=%d target=%s", errDepthLimitReached, rel, depth, target.String())
	}
	childParked, err := w.shrinkLiveChildrenToParent(rel, target, depth+1)
	if err != nil {
		return parked, err
	}
	parked = parked.Union(childParked)
	// A parked descendant bucket owns CPUs outside target; this intermediate
	// parent must retain them too or the parked child would fall outside its
	// parent and violate the cgroup v1 parent-superset rule.
	effectiveTarget := target.Union(parked)
	if err := w.writeDynamicRel(rel, effectiveTarget, ""); err != nil {
		return parked, err
	}
	return parked, nil
}

func (w safeCPSetWriter) writeDynamicRel(rel string, target machine.CPUSet, mems string) error {
	if resolved, resolvedMems, guarded := w.resolveDynamicRelTarget(rel, target); guarded {
		target = resolved
		if resolvedMems != "" {
			mems = resolvedMems
		}
	}
	if w.cg.Version(w.ctx) == cgroupclient.CgroupVersionV1 && w.hasControlledAncestor(rel) {
		if err := w.ensureReclaimDescendantParentContains(rel, target, mems); err != nil {
			return err
		}
	}
	for attempt := 0; attempt < maxSafeCPUSetWriteAttempts; attempt++ {
		if w.res != nil {
			w.res.Attempted++
		}
		err := applyCPUSet(w.ctx, w.cg, rel, target, mems)
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
		if _, shrinkErr := w.shrinkLiveChildrenToParent(rel, target, 0); shrinkErr != nil {
			return shrinkErr
		}
	}
	return nil
}

func (w safeCPSetWriter) resolveDynamicRelTarget(rel string, requested machine.CPUSet) (machine.CPUSet, string, bool) {
	bucket := w.dynamicPolicy.findAncestorReclaimBucket(rel)
	if bucket == nil {
		return w.resolveDynamicRelTargetByPhysicalBucket(rel, requested)
	}
	if rel == bucket.Rel {
		return requested, "", false
	}
	bucketTarget, ok := w.targetByRel[bucket.Rel]
	if !ok {
		bucketTarget = bucket.CPUs
	}
	boundary := bucketTarget
	if bucketCurrent, err := w.cg.ReadCPUSet(w.ctx, bucket.Rel); err == nil && !bucketCurrent.IsEmpty() {
		if overlap := bucketTarget.Intersection(bucketCurrent); !overlap.IsEmpty() {
			boundary = overlap
		} else {
			boundary = bucketCurrent
		}
	}
	desired := requested.Intersection(boundary)
	if desired.IsEmpty() {
		desired = boundary
	}
	return desired, memsForNode(bucket, w.defaultMems), true
}

func (w safeCPSetWriter) resolveDynamicRelTargetByPhysicalBucket(rel string, requested machine.CPUSet) (machine.CPUSet, string, bool) {
	bucketRel, numaID, ok := findPhysicalReclaimBucketRel(rel)
	if !ok || bucketRel == rel {
		return requested, "", false
	}
	bucketCurrent, err := w.cg.ReadCPUSet(w.ctx, bucketRel)
	if err != nil || bucketCurrent.IsEmpty() {
		return requested, "", false
	}
	desired := requested.Intersection(bucketCurrent)
	if desired.IsEmpty() {
		desired = bucketCurrent
	}
	return desired, strconv.Itoa(numaID), true
}

func findPhysicalReclaimBucketRel(rel string) (string, int, bool) {
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		if !strings.HasPrefix(part, "reclaimed-") {
			continue
		}
		numaID, err := strconv.Atoi(strings.TrimPrefix(part, "reclaimed-"))
		if err != nil {
			continue
		}
		return strings.Join(parts[:i+1], "/"), numaID, true
	}
	return "", 0, false
}

// parentSupersetHeld reports whether the parent node's current cgroup cpuset
// already covers the union of every live child. This is the safety precondition
// for deferring a final parent shrink: the bridge must have been written so the
// parent stays a valid cgroup v1 superset, and only the final narrowing to the
// steady-state target is what could not complete this pass.
func (w safeCPSetWriter) parentSupersetHeld(node *TopoNode) bool {
	if node == nil {
		return false
	}
	liveChildUnion, err := w.liveChildUnion(node.Rel)
	if err != nil {
		return false
	}
	current, err := w.cg.ReadCPUSet(w.ctx, node.Rel)
	if err != nil {
		return false
	}
	return liveChildUnion.IsSubsetOf(current)
}

// finalParentShrink performs the last narrowing write of a parent node to its
// steady-state effectiveTarget. When the write keeps hitting EBUSY because a
// controlled child bucket still holds a near-disjoint previous generation (RC1
// downstream lag), it does NOT surface a hard EBUSY that would fail Pod
// admission. Instead, provided the parent is still a valid superset of every
// live child, it returns errDeferConvergence so the next periodical reconcile
// can finish the shrink after the advisor moves the child bucket. Any other
// error, or an illegal parent-below-child state, is returned unchanged.
func (w safeCPSetWriter) finalParentShrink(node *TopoNode, effectiveTarget machine.CPUSet) error {
	err := w.writeNode(node, effectiveTarget)
	if err == nil {
		return nil
	}
	if isCgroupBusyError(err) && w.parentSupersetHeld(node) {
		general.Warningf("topo_dag_writer: defer_convergence rel=%q target=%s err=%v",
			node.Rel, effectiveTarget.String(), err)
		return errDeferConvergence
	}
	return err
}

func (w safeCPSetWriter) writeNode(node *TopoNode, target machine.CPUSet) error {
	if node == nil {
		return nil
	}
	keptParentBridge, err := w.keepParentBridgeForControlledChildren(node, target)
	if err != nil {
		return err
	}
	if keptParentBridge {
		return nil
	}
	keptLiveBridge, err := w.keepParentBridgeForLiveChildren(node, target)
	if err != nil {
		return err
	}
	if keptLiveBridge {
		return nil
	}
	if w.constrainBridge && !w.liveChildrenCoveredByTarget(node.Rel, target) {
		if err := w.convergeLiveChildrenBeforeConstrainedWrite(node.Rel, target); err != nil {
			return err
		}
	}
	// EBUSY means the kernel observed a hierarchy that no longer matches the
	// last snapshot. Retry only EBUSY, reconcile live children before the next
	// attempt, and keep retries bounded so persistent ownership errors remain
	// visible to the periodical retry path.
	for attempt := 0; attempt < maxSafeCPUSetWriteAttempts; attempt++ {
		w.logControlledNodeWrite("write_node", node, target, machine.NewCPUSet(), attempt)
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
		w.logCPUSetSubtreeOnWriteFailure("write_node", node, target, machine.NewCPUSet(), attempt, err)
		if !isCgroupBusyError(err) || attempt == maxSafeCPUSetWriteAttempts-1 {
			return err
		}
		if err := w.reconcileLiveChildrenBeforeRetry(node.Rel, target); err != nil {
			return err
		}
	}
	return nil
}

func (w safeCPSetWriter) keepParentBridgeForControlledChildren(node *TopoNode, target machine.CPUSet) (bool, error) {
	if !w.hasControlledDirectChild(node.Rel) {
		return false, nil
	}
	controlledChildUnion, err := w.controlledChildUnion(node.Rel)
	if err != nil {
		return false, err
	}
	if controlledChildUnion.IsSubsetOf(target) {
		return false, nil
	}
	parentActual, err := w.cg.ReadCPUSet(w.ctx, node.Rel)
	if err != nil {
		return false, fmt.Errorf("read parent cpuset before controlled-child bridge write, rel=%s target=%s: %w", node.Rel, target.String(), err)
	}
	bridgeTarget := parentActual.Union(controlledChildUnion)
	if !parentActual.Equals(bridgeTarget) {
		if err := w.writeBridgeNode(node, target, bridgeTarget); err != nil {
			return false, err
		}
	}
	general.Infof("topo_dag_writer: keep_parent_bridge_for_controlled_children rel=%q target=%s controlledChildUnion=%s bridgeTarget=%s",
		node.Rel, target.String(), controlledChildUnion.String(), bridgeTarget.String())
	return true, nil
}

func (w safeCPSetWriter) keepParentBridgeForLiveChildren(node *TopoNode, target machine.CPUSet) (bool, error) {
	if node.Role != TopoNodeRoleReclaimNUMABucket {
		return false, nil
	}
	liveChildUnion, err := w.liveChildUnion(node.Rel)
	if err != nil {
		return false, err
	}
	if liveChildUnion.IsSubsetOf(target) {
		return false, nil
	}
	parentActual, err := w.cg.ReadCPUSet(w.ctx, node.Rel)
	if err != nil {
		return false, fmt.Errorf("read parent cpuset before live-child bridge write, rel=%s target=%s: %w", node.Rel, target.String(), err)
	}
	bridgeTarget := parentActual.Union(liveChildUnion)
	if !parentActual.Equals(bridgeTarget) {
		if err := w.writeBridgeNode(node, target, bridgeTarget); err != nil {
			return false, err
		}
	}
	general.Infof("topo_dag_writer: keep_parent_bridge_for_live_children rel=%q target=%s liveChildUnion=%s bridgeTarget=%s",
		node.Rel, target.String(), liveChildUnion.String(), bridgeTarget.String())
	return true, nil
}

func (w safeCPSetWriter) hasControlledDirectChild(parentRel string) bool {
	prefix := strings.Trim(parentRel, "/") + "/"
	for rel := range w.controlledByRel {
		trimmed := strings.Trim(rel, "/")
		if !strings.HasPrefix(trimmed, prefix) {
			continue
		}
		if !strings.Contains(strings.TrimPrefix(trimmed, prefix), "/") {
			return true
		}
	}
	return false
}

func (w safeCPSetWriter) hasControlledAncestor(rel string) bool {
	trimmedRel := strings.Trim(rel, "/")
	for controlledRel := range w.controlledByRel {
		trimmedControlled := strings.Trim(controlledRel, "/")
		if trimmedControlled != "" && strings.HasPrefix(trimmedRel, trimmedControlled+"/") {
			return true
		}
	}
	return false
}

func (w safeCPSetWriter) convergeLiveChildrenBeforeConstrainedWrite(parentRel string, target machine.CPUSet) error {
	for attempt := 0; attempt < maxLiveChildShrinkAttempts; attempt++ {
		liveChildUnion, err := w.liveChildUnion(parentRel)
		if err != nil {
			return err
		}
		if liveChildUnion.IsSubsetOf(target) {
			return nil
		}
		if _, err := w.shrinkLiveChildrenToParent(parentRel, target, 0); err != nil {
			return err
		}
	}
	liveChildUnion, err := w.liveChildUnion(parentRel)
	if err != nil {
		return err
	}
	if liveChildUnion.IsSubsetOf(target) {
		return nil
	}
	general.Warningf("topo_dag_writer: defer_convergence_prewrite_live_children rel=%q target=%s liveChildUnion=%s",
		parentRel, target.String(), liveChildUnion.String())
	return errDeferConvergence
}

func (w safeCPSetWriter) liveChildrenCoveredByTarget(parentRel string, target machine.CPUSet) bool {
	liveChildUnion, err := w.liveChildUnion(parentRel)
	return err == nil && liveChildUnion.IsSubsetOf(target)
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
		w.logControlledNodeWrite("write_bridge_node", node, target, finalTarget, attempt)
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
		w.logCPUSetSubtreeOnWriteFailure("write_bridge_node", node, target, finalTarget, attempt, err)
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

func (w safeCPSetWriter) logControlledNodeWrite(stage string, node *TopoNode, target machine.CPUSet, finalTarget machine.CPUSet, attempt int) {
	if node == nil || !klog.V(4).Enabled() {
		return
	}
	current := "<read_error>"
	if cur, err := w.cg.ReadCPUSet(w.ctx, node.Rel); err == nil {
		current = cur.String()
	}
	targetByRel := "<missing>"
	if target, ok := w.targetByRel[node.Rel]; ok {
		targetByRel = target.String()
	}
	parentRel := ""
	parentCurrent := ""
	if parent := parentNodeOf(node); parent != nil {
		parentRel = parent.Rel
		parentCurrent = "<read_error>"
		if cur, err := w.cg.ReadCPUSet(w.ctx, parent.Rel); err == nil {
			parentCurrent = cur.String()
		}
	}
	general.InfofV(4, "topo_dag_writer: controlled_write stage=%s rel=%q role=%v parent=%q attempt=%d current=%s parentCurrent=%s target=%s finalTarget=%s targetByRel=%s nodeCPUs=%s mems=%q metadata=%v",
		stage, node.Rel, node.Role, parentRel, attempt, current, parentCurrent, target.String(), finalTarget.String(), targetByRel, node.CPUs.String(), memsForNode(node, w.defaultMems), node.Metadata)
}

func (w safeCPSetWriter) logCPUSetSubtreeOnWriteFailure(stage string, node *TopoNode, target machine.CPUSet, finalTarget machine.CPUSet, attempt int, writeErr error) {
	if node == nil {
		return
	}
	entries, truncated := w.collectCPUSetSubtreeSnapshot(node.Rel)
	general.Warningf("topo_dag_writer: cpuset_write_failed stage=%s rel=%q role=%v attempt=%d retryable=%t target=%s finalTarget=%s mems=%q metadata=%v err=%v subtree_truncated=%v subtree=[%s]",
		stage, node.Rel, node.Role, attempt, isCgroupBusyError(writeErr), target.String(), finalTarget.String(), memsForNode(node, w.defaultMems), node.Metadata, writeErr, truncated, strings.Join(entries, " ; "))
}

type cpusetSubtreeSnapshotItem struct {
	rel   string
	depth int
}

func (w safeCPSetWriter) collectCPUSetSubtreeSnapshot(rootRel string) ([]string, bool) {
	queue := []cpusetSubtreeSnapshotItem{{rel: rootRel, depth: 0}}
	entries := make([]string, 0, maxCPUSetFailureDumpNodes)
	truncated := false

	for len(queue) > 0 {
		item := queue[0]
		queue = queue[1:]
		if len(entries) >= maxCPUSetFailureDumpNodes {
			truncated = true
			break
		}

		entries = append(entries, w.formatCPUSetSubtreeSnapshotEntry(item.rel))
		if item.depth >= maxCPUSetFailureDumpDepth {
			continue
		}

		children, err := w.cg.ListChildren(w.ctx, item.rel)
		if err != nil {
			entries = append(entries, fmt.Sprintf("rel=%q children=<read_error:%v>", item.rel, err))
			continue
		}
		for _, child := range children {
			queue = append(queue, cpusetSubtreeSnapshotItem{
				rel:   filepath.Join(item.rel, child),
				depth: item.depth + 1,
			})
		}
	}

	return entries, truncated
}

func (w safeCPSetWriter) formatCPUSetSubtreeSnapshotEntry(rel string) string {
	fields := []string{
		fmt.Sprintf("rel=%q", rel),
		fmt.Sprintf("cpus=%q", w.readCgroupFileForLog(rel, "cpuset.cpus")),
		fmt.Sprintf("mems=%q", w.readCgroupFileForLog(rel, "cpuset.mems")),
	}
	switch w.cg.Version(w.ctx) {
	case cgroupclient.CgroupVersionV1:
		fields = append(fields, fmt.Sprintf("slb=%q", w.readCgroupFileForLog(rel, "cpuset.sched_load_balance")))
	case cgroupclient.CgroupVersionV2:
		fields = append(fields, fmt.Sprintf("partition=%q", w.readCgroupFileForLog(rel, "cpuset.cpus.partition")))
	}
	fields = append(fields,
		fmt.Sprintf("tasks=%q", w.readCgroupFileForLog(rel, "tasks")),
		fmt.Sprintf("procs=%q", w.readCgroupFileForLog(rel, "cgroup.procs")),
	)
	return strings.Join(fields, " ")
}

func (w safeCPSetWriter) readCgroupFileForLog(rel, file string) string {
	raw, err := w.cg.ReadCgroupFile(w.ctx, rel, file)
	if err != nil {
		return fmt.Sprintf("<read_error:%v>", err)
	}
	return summarizeCgroupFileForLog(raw)
}

func summarizeCgroupFileForLog(raw []byte) string {
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return "<empty>"
	}
	if len(fields) > maxCgroupFileLogFields {
		return fmt.Sprintf("%s...(+%d)", strings.Join(fields[:maxCgroupFileLogFields], ","), len(fields)-maxCgroupFileLogFields)
	}
	return strings.Join(fields, ",")
}

func (w safeCPSetWriter) reconcileLiveChildrenBeforeRetry(parentRel string, parentTarget machine.CPUSet) error {
	liveChildUnion, err := w.liveChildUnion(parentRel)
	if err != nil {
		return err
	}
	if liveChildUnion.IsSubsetOf(parentTarget) {
		return nil
	}
	// Reconcile is best-effort before an EBUSY retry; any parked cross-NUMA
	// bucket is left for the parent's own final write to retain as a superset.
	_, err = w.shrinkLiveChildrenToParent(parentRel, parentTarget, 0)
	return err
}

func isCgroupBusyError(err error) bool {
	if errors.Is(err, syscall.EBUSY) {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "device or resource busy")
}
