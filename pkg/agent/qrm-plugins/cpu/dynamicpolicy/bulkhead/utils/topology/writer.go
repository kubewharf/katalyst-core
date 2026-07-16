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
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	maxShrinkRetries = 4
	maxEnforceDepth  = 8
)

type DAGApplyResult struct {
	Attempted int
	Applied   int
	Skipped   int
	Failed    int
}

type DAGApplyInputs struct {
	DAG    *TopoDAG
	Cgroup cgroupclient.CgroupClient
	Mems   string
	// SkipObservedRead treats all non-empty target nodes as expand-only writes.
	// Callers should only use it when the input is already known to be monotonic.
	SkipObservedRead    bool
	ExpectedCPUSetByRel map[string]machine.CPUSet
	// KubeManagedRelPrefix scopes Kubernetes-managed subtree handling to the
	// configured primary rel path. Empty prefix falls back to the DAG primary rel.
	KubeManagedRelPrefix string
	// ProtectedPendingCPUSet is the union of container allocations that already
	// exist in QRM state but whose cgroup leaf has not been created yet (pod
	// admit window). These have no resolvable rel, so the writer folds them into
	// the primary node's effective target to guarantee the primary cgroup never
	// shrinks below an allocation that is about to materialize.
	ProtectedPendingCPUSet machine.CPUSet
}

func ApplyDAGDiff(ctx context.Context, in DAGApplyInputs) (DAGApplyResult, error) {
	res := DAGApplyResult{}
	if in.DAG == nil {
		return res, errors.New("ApplyDAGDiff: nil DAG")
	}
	if in.Cgroup == nil {
		return res, errors.New("ApplyDAGDiff: nil Cgroup client")
	}
	err := applyTwoPhase(ctx, in, &res)
	return res, err
}

type nodeDiff struct {
	grow          bool
	shrink        bool
	target        machine.CPUSet
	shrinkTarget  machine.CPUSet
	observed      machine.CPUSet
	observedKnown bool
}

type siblingTransition struct {
	node                *TopoNode
	observed            machine.CPUSet
	target              machine.CPUSet
	leavingToSibling    machine.CPUSet
	enteringFromSibling machine.CPUSet
	preShrinkTarget     machine.CPUSet
}

func applyTwoPhase(ctx context.Context, in DAGApplyInputs, res *DAGApplyResult) error {
	dag := in.DAG
	cg := in.Cgroup
	expected := in.ExpectedCPUSetByRel
	kubeRelPrefix := in.KubeManagedRelPrefix
	version := cg.Version(ctx)
	allowEmptyTarget := version == cgroupclient.CgroupVersionV2
	diffs := map[string]nodeDiff{}
	controlledRels := map[string]struct{}{}
	for _, n := range dag.Nodes() {
		controlledRels[n.Rel] = struct{}{}
	}
	if strings.Trim(kubeRelPrefix, "/") == "" {
		kubeRelPrefix = primaryRelPath(dag)
	}

	// A single applyCache is shared by computeEffectiveTargets, shrink follow-up,
	// and expandDescendants so that every (rel -> children) and
	// (rel -> expectedDescendantUnion) question is answered at most once per
	// apply.
	cache := newApplyCache(cg, kubeRelPrefix, expected)

	// Build the effective target for every controlled node before touching any
	// cgroup. Pending primary allocations widen the primary target; existing kube
	// leaves and stale runtime residuals are converged by the shrink/expand
	// descendant pass rather than by effective-target widening.
	effectiveTargets := desiredTargets(dag)
	if !in.SkipObservedRead {
		var err error
		effectiveTargets, err = computeEffectiveTargets(dag, allowEmptyTarget, in.ProtectedPendingCPUSet)
		if err != nil {
			return err
		}
	}

	for _, n := range dag.Nodes() {
		target := effectiveTargets[n.Rel]
		var observed machine.CPUSet
		observedKnown := false
		if !in.SkipObservedRead {
			if cs, readErr := cg.ReadCPUSet(ctx, n.Rel); readErr == nil {
				observed = cs
				observedKnown = true
			}
		}
		if target.IsEmpty() && !allowEmptyTarget {
			if observedKnown && !observed.IsEmpty() {
				diffs[n.Rel] = nodeDiff{target: target, observed: observed, observedKnown: true}
			}
			res.Skipped++
			continue
		}
		if observedKnown && observed.Equals(target) {
			res.Skipped++
			continue
		}
		d := nodeDiff{target: target, observed: observed, observedKnown: observedKnown}
		if in.SkipObservedRead || !observedKnown || observed.IsEmpty() {
			d.grow = true
		} else {
			if !target.IsSubsetOf(observed) {
				d.grow = true
			}
			if !observed.IsSubsetOf(target) {
				d.shrink = true
				d.shrinkTarget = target
			}
			if d.grow && d.shrink {
				intersection := observed.Intersection(target)
				if intersection.IsEmpty() {
					if ok, reason := allowReclaimNUMABucketDisjointReplacement(dag, n, target, effectiveTargets); ok {
						d.shrinkTarget = target
						d.grow = false
						general.InfofV(4, "topo_dag_writer: allow safe reclaim numa disjoint replacement, rel=%q observed=%s target=%s reason=%s",
							n.Rel, observed.String(), target.String(), reason)
					} else {
						return fmt.Errorf("ApplyDAGDiff: disjoint cpuset change @ %s observed=%s target=%s reason=%s", n.Rel, observed.String(), target.String(), reason)
					}
				} else {
					d.shrinkTarget = intersection
				}
			}
		}
		diffs[n.Rel] = d
	}
	general.InfofV(5, "topo_dag_writer: diff skip_observed=%t protected_pending=%s kube_prefix=%q effective=%s diffs=%s",
		in.SkipObservedRead, in.ProtectedPendingCPUSet.String(), kubeRelPrefix,
		formatCPUSetMapForLog(effectiveTargets, 16), formatNodeDiffsForLog(diffs, 16))

	var firstErr error
	failedSiblingLeaving := machine.NewCPUSet()
	if !in.SkipObservedRead {
		transitions := buildSiblingTransitions(dag, diffs, effectiveTargets)
		failedSiblingLeaving = applySiblingPreShrink(ctx, cg, in, diffs, transitions, version, res, cache)
	}
	// sched_load_balance disabled cpuset domains must not overlap transiently.
	// Apply changes in two phases: shrink in post-order first, then expand in
	// pre-order. Overlap replacements are decomposed as observed->intersection
	// then intersection->target; disjoint jumps are rejected above.
	_ = dag.ForEachShrink(func(n *TopoNode) error {
		d, ok := diffs[n.Rel]
		if !ok || !d.shrink {
			return nil
		}
		res.Attempted++
		if err := applyCPUSet(ctx, cg, n.Rel, d.shrinkTarget, memsForNode(n, in.Mems)); err == nil {
			res.Applied++
			return nil
		}
		if err := shrinkNodeConverge(ctx, cg, n.Rel, d.shrinkTarget, memsForNode(n, in.Mems), expected, version, res, 0, cache); err != nil {
			res.Failed++
			if firstErr == nil {
				firstErr = err
			}
			return nil
		}
		res.Applied++
		return nil
	})
	_ = dag.ForEachExpand(func(n *TopoNode) error {
		effTarget := effectiveTargets[n.Rel]
		if d, ok := diffs[n.Rel]; ok && d.grow {
			target := safeSiblingGrowTarget(n, d.target, failedSiblingLeaving)
			if target.IsEmpty() && !allowEmptyTarget {
				res.Skipped++
				general.InfofV(5, "topo_dag_writer: skip empty v1 sibling grow target, rel=%q original_target=%s failed_leaving=%s",
					n.Rel, d.target.String(), failedSiblingLeaving.String())
				return nil
			}
			res.Attempted++
			if err := applyCPUSet(ctx, cg, n.Rel, target, memsForNode(n, in.Mems)); err != nil {
				res.Failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("apply cpuset.cpus=%s @ %s: %w", target.String(), n.Rel, err)
				}
				// The node's own grow write failed, so its cpuset is still at the
				// smaller observed value. Descending now would write children to
				// the (larger) effective target and violate the cgroup v1
				// parent-superset invariant, mirroring the fail-fast guard in
				// writeAndDescend. Skip this subtree; the next round retries.
				general.InfofV(5, "topo_dag_writer: skip expand descent after node grow failure, rel=%q target=%s", n.Rel, target.String())
				return nil
			}
			res.Applied++
			if !target.Equals(d.target) {
				effTarget = target
			}
		}
		if !effTarget.IsEmpty() || allowEmptyTarget {
			expandDescendants(ctx, cg, n.Rel, effTarget, false, controlledRels, expected, allowEmptyTarget, res, &firstErr, 0, cache)
		}
		return nil
	})
	return firstErr
}

func isSiblingDomainNode(n *TopoNode) bool {
	if n == nil {
		return false
	}
	switch n.Role {
	case TopoNodeRolePrimary, TopoNodeRoleReclaim, TopoNodeRoleReclaimSibling:
		return true
	default:
		return false
	}
}

func siblingDomainNodes(dag *TopoDAG) []*TopoNode {
	if dag == nil {
		return nil
	}
	nodes := make([]*TopoNode, 0)
	for _, n := range dag.Nodes() {
		if isSiblingDomainNode(n) {
			nodes = append(nodes, n)
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		return nodes[i].Rel < nodes[j].Rel
	})
	return nodes
}

func buildSiblingTransitions(dag *TopoDAG, diffs map[string]nodeDiff, effectiveTargets map[string]machine.CPUSet) map[string]siblingTransition {
	nodes := siblingDomainNodes(dag)
	if len(nodes) <= 1 {
		return nil
	}

	transitions := map[string]siblingTransition{}
	for _, n := range nodes {
		d, ok := diffs[n.Rel]
		if !ok || !d.observedKnown {
			continue
		}

		otherObservedUnion := machine.NewCPUSet()
		otherTargetUnion := machine.NewCPUSet()
		for _, other := range nodes {
			if other.Rel == n.Rel {
				continue
			}
			if od, ok := diffs[other.Rel]; ok && od.observedKnown {
				otherObservedUnion = otherObservedUnion.Union(od.observed)
			}
			if target, ok := effectiveTargets[other.Rel]; ok {
				otherTargetUnion = otherTargetUnion.Union(target)
			}
		}

		target := effectiveTargets[n.Rel]
		leaving := d.observed.Difference(target)
		entering := target.Difference(d.observed)
		t := siblingTransition{
			node:                n,
			observed:            d.observed,
			target:              target,
			leavingToSibling:    leaving.Intersection(otherTargetUnion),
			enteringFromSibling: entering.Intersection(otherObservedUnion),
		}
		t.preShrinkTarget = t.observed.Difference(t.leavingToSibling)
		if !t.leavingToSibling.IsEmpty() || !t.enteringFromSibling.IsEmpty() {
			transitions[n.Rel] = t
		}
	}
	return transitions
}

func applySiblingPreShrink(
	ctx context.Context,
	cg cgroupclient.CgroupClient,
	in DAGApplyInputs,
	diffs map[string]nodeDiff,
	transitions map[string]siblingTransition,
	version cgroupclient.CgroupVersion,
	res *DAGApplyResult,
	cache *applyCache,
) machine.CPUSet {
	failedLeaving := machine.NewCPUSet()
	if len(transitions) == 0 {
		return failedLeaving
	}

	rels := make([]string, 0, len(transitions))
	for rel := range transitions {
		rels = append(rels, rel)
	}
	sort.Strings(rels)

	for _, rel := range rels {
		t := transitions[rel]
		if t.leavingToSibling.IsEmpty() || t.preShrinkTarget.Equals(t.observed) {
			continue
		}
		if t.preShrinkTarget.IsEmpty() && version == cgroupclient.CgroupVersionV1 {
			failedLeaving = failedLeaving.Union(t.leavingToSibling)
			general.InfofV(4, "topo_dag_writer: guard sibling grow after empty v1 pre-shrink, rel=%q leaving=%s",
				rel, t.leavingToSibling.String())
			continue
		}

		res.Attempted++
		err := applyCPUSet(ctx, cg, rel, t.preShrinkTarget, memsForNode(t.node, in.Mems))
		if err != nil {
			err = shrinkNodeConverge(ctx, cg, rel, t.preShrinkTarget, memsForNode(t.node, in.Mems),
				in.ExpectedCPUSetByRel, version, res, 0, cache)
		}
		if err != nil {
			res.Failed++
			failedLeaving = failedLeaving.Union(t.leavingToSibling)
			general.InfofV(4, "topo_dag_writer: guard sibling grow after pre-shrink failure, rel=%q target=%s leaving=%s err=%v",
				rel, t.preShrinkTarget.String(), t.leavingToSibling.String(), err)
			continue
		}

		res.Applied++
		if d, ok := diffs[rel]; ok {
			diffs[rel] = recomputeNodeDiffWithObserved(d, t.preShrinkTarget)
		}
	}
	return failedLeaving
}

func recomputeNodeDiffWithObserved(d nodeDiff, observed machine.CPUSet) nodeDiff {
	d.observed = observed
	d.observedKnown = true
	d.grow = false
	d.shrink = false
	d.shrinkTarget = machine.NewCPUSet()
	if !d.target.IsSubsetOf(observed) {
		d.grow = true
	}
	if !observed.IsSubsetOf(d.target) {
		d.shrink = true
		d.shrinkTarget = d.target
	}
	if d.grow && d.shrink {
		intersection := observed.Intersection(d.target)
		if !intersection.IsEmpty() {
			d.shrinkTarget = intersection
		}
	}
	return d
}

func safeSiblingGrowTarget(n *TopoNode, target machine.CPUSet, failedLeaving machine.CPUSet) machine.CPUSet {
	if !isSiblingDomainNode(n) || failedLeaving.IsEmpty() {
		return target
	}
	return target.Difference(failedLeaving)
}

func memsForNode(n *TopoNode, defaultMems string) string {
	if n != nil && n.Mems != "" {
		return n.Mems
	}
	return defaultMems
}

func applyCPUSet(ctx context.Context, cg cgroupclient.CgroupClient, rel string, cpus machine.CPUSet, mems string) error {
	data := &cgcommon.CPUSetData{CPUs: cpus.String()}
	if cpus.IsEmpty() && cg.Version(ctx) == cgroupclient.CgroupVersionV2 {
		data.WriteEmptyCPUs = true
	}
	if mems != "" {
		data.Mems = mems
	}
	general.InfofV(6, "topo_dag_writer: cpuset_write start rel=%q target=%s mems=%q", rel, cpus.String(), mems)
	if err := cg.ApplyCPUSet(ctx, rel, data); err != nil {
		general.InfofV(4, "topo_dag_writer: cpuset_write failed rel=%q target=%s mems=%q err=%v", rel, cpus.String(), mems, err)
		return fmt.Errorf("apply cpuset.cpus=%s @ %s: %w", cpus.String(), rel, err)
	}
	general.InfofV(6, "topo_dag_writer: cpuset_write done rel=%q target=%s mems=%q", rel, cpus.String(), mems)
	return nil
}

func formatCPUSetMapForLog(m map[string]machine.CPUSet, limit int) string {
	if len(m) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if limit <= 0 || limit > len(keys) {
		limit = len(keys)
	}
	parts := make([]string, 0, limit+1)
	for _, k := range keys[:limit] {
		parts = append(parts, fmt.Sprintf("%s=%s", k, m[k].String()))
	}
	if len(keys) > limit {
		parts = append(parts, fmt.Sprintf("...(+%d)", len(keys)-limit))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func formatNodeDiffsForLog(diffs map[string]nodeDiff, limit int) string {
	if len(diffs) == 0 {
		return "{}"
	}
	keys := make([]string, 0, len(diffs))
	for k := range diffs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if limit <= 0 || limit > len(keys) {
		limit = len(keys)
	}
	parts := make([]string, 0, limit+1)
	for _, k := range keys[:limit] {
		d := diffs[k]
		observed := "unknown"
		if d.observedKnown {
			observed = d.observed.String()
		}
		parts = append(parts, fmt.Sprintf("%s{observed=%s,target=%s,shrinkTarget=%s,grow=%t,shrink=%t}",
			k, observed, d.target.String(), d.shrinkTarget.String(), d.grow, d.shrink))
	}
	if len(keys) > limit {
		parts = append(parts, fmt.Sprintf("...(+%d)", len(keys)-limit))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func isCgroupNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR) {
		return true
	}
	errText := strings.ToLower(err.Error())
	return strings.Contains(errText, "no such file or directory") ||
		strings.Contains(errText, "not a directory")
}

func shrinkNodeConverge(ctx context.Context, cg cgroupclient.CgroupClient, relPath string, newSelf machine.CPUSet, mems string, expected map[string]machine.CPUSet, version cgroupclient.CgroupVersion, res *DAGApplyResult, depth int, cache *applyCache) error {
	if depth >= maxEnforceDepth {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < maxShrinkRetries; attempt++ {
		liveChildren := attempt > 0
		if !newSelf.IsEmpty() {
			cur, readErr := cg.ReadCPUSet(ctx, relPath)
			if readErr != nil {
				general.InfofV(5, "topo_dag_writer: read cpuset before shrink failed, rel=%q target=%s err=%v",
					relPath, newSelf.String(), readErr)
			} else if !newSelf.IsSubsetOf(cur) {
				widened := cur.Union(newSelf)
				if err := applyCPUSet(ctx, cg, relPath, widened, ""); err != nil {
					general.InfofV(5, "topo_dag_writer: expand intermediate before shrink failed, rel=%q cur=%s target=%s widened=%s err=%v",
						relPath, cur.String(), newSelf.String(), widened.String(), err)
				} else {
					general.InfofV(5, "topo_dag_writer: expand intermediate before shrink, rel=%q cur=%s target=%s widened=%s",
						relPath, cur.String(), newSelf.String(), widened.String())
				}
			}
		}
		shrinkDescendantsToParent(ctx, cg, relPath, newSelf, expected, version, res, depth+1, cache, liveChildren)
		err := applyCPUSet(ctx, cg, relPath, newSelf, mems)
		if err == nil {
			return nil
		}
		lastErr = err
		// A child sitting on a cpuset disjoint from the new parent target cannot
		// be clamped into the parent, so retrying will keep hitting the same
		// kernel rejection. Surface the blocking children immediately instead of
		// exhausting the retry budget (which keeps healthz red for longer).
		blockers := collectShrinkBlockers(ctx, cg, relPath, newSelf, expected, maxShrinkBlockers, 0, cache, true)
		if hasDisjointBlocker(blockers) {
			general.InfofV(4, "topo_dag_writer: shrink blocked by disjoint child, rel=%q newSelf=%s blockers=%s", relPath, newSelf.String(), formatShrinkBlockers(blockers))
			return fmt.Errorf("shrink blocked @ %s target=%s blockers=%s: %w", relPath, newSelf.String(), formatShrinkBlockers(blockers), lastErr)
		}
	}
	blockers := collectShrinkBlockers(ctx, cg, relPath, newSelf, expected, maxShrinkBlockers, 0, cache, true)
	general.InfofV(4, "topo_dag_writer: shrink converge exhausted, rel=%q newSelf=%s blockers=%s err=%v", relPath, newSelf.String(), formatShrinkBlockers(blockers), lastErr)
	if len(blockers) > 0 {
		return fmt.Errorf("shrink converge exhausted @ %s target=%s blockers=%s: %w", relPath, newSelf.String(), formatShrinkBlockers(blockers), lastErr)
	}
	return fmt.Errorf("shrink converge exhausted @ %s: %w", relPath, lastErr)
}

type shrinkBlocker struct {
	Rel      string
	Current  machine.CPUSet
	Expected *machine.CPUSet
	Reason   string
}

const (
	shrinkBlockerReasonDisjoint        = "current_disjoint_parent"
	shrinkBlockerReasonExpectedOutside = "expected_outside_parent"
	shrinkBlockerReasonCurrentOutside  = "current_outside_parent"
	shrinkBlockerReasonReadError       = "read_error"
	maxShrinkBlockers                  = 8
)

func hasDisjointBlocker(blockers []shrinkBlocker) bool {
	for _, b := range blockers {
		if b.Reason == shrinkBlockerReasonDisjoint {
			return true
		}
	}
	return false
}

func listShrinkChildren(ctx context.Context, cg cgroupclient.CgroupClient, relPath string, cache *applyCache, liveChildren bool) ([]string, error) {
	if !liveChildren && cache != nil {
		return cache.listChildren(ctx, relPath)
	}
	return cg.ListChildren(ctx, relPath)
}

// collectShrinkBlockers walks the descendants of relPath and records children
// whose current (or expected) cpuset prevents relPath from shrinking to
// newParent. It stops once limit blockers are collected to bound log size.
//
// The first shrink attempt may use the per-apply children memo for speed. Once
// the parent write fails, callers switch liveChildren to true so diagnostics
// re-list live children and can see cgroups created after the cache snapshot.
func collectShrinkBlockers(ctx context.Context, cg cgroupclient.CgroupClient, relPath string, newParent machine.CPUSet, expected map[string]machine.CPUSet, limit, depth int, cache *applyCache, liveChildren bool) []shrinkBlocker {
	if depth >= maxEnforceDepth || limit <= 0 {
		return nil
	}
	children, err := listShrinkChildren(ctx, cg, relPath, cache, liveChildren)
	if err != nil {
		return nil
	}
	var blockers []shrinkBlocker
	for _, name := range children {
		if len(blockers) >= limit {
			break
		}
		childRel := filepath.Join(relPath, name)
		cur, readErr := cg.ReadCPUSet(ctx, childRel)
		if readErr != nil {
			// A child we cannot read is itself a potential blocker: if it holds a
			// cpuset outside newParent the shrink will keep failing, so surface it
			// rather than silently dropping it.
			blockers = append(blockers, shrinkBlocker{Rel: childRel, Reason: shrinkBlockerReasonReadError})
			continue
		}
		if cur.IsEmpty() || cur.IsSubsetOf(newParent) {
			blockers = append(blockers, collectShrinkBlockers(ctx, cg, childRel, newParent, expected, limit-len(blockers), depth+1, cache, liveChildren)...)
			continue
		}
		b := shrinkBlocker{Rel: childRel, Current: cur}
		if exp, ok := expected[childRel]; ok {
			expCopy := exp.Clone()
			b.Expected = &expCopy
			if !exp.IsSubsetOf(newParent) {
				b.Reason = shrinkBlockerReasonExpectedOutside
			}
		}
		if cur.Intersection(newParent).IsEmpty() {
			b.Reason = shrinkBlockerReasonDisjoint
		} else if b.Reason == "" {
			// current overlaps but is not fully inside newParent, and there is no
			// expected entry pulling it out: the live cpuset itself straddles the
			// parent boundary.
			b.Reason = shrinkBlockerReasonCurrentOutside
		}
		blockers = append(blockers, b)
	}
	return blockers
}

func formatShrinkBlockers(blockers []shrinkBlocker) string {
	if len(blockers) == 0 {
		return "[]"
	}
	var sb strings.Builder
	sb.WriteString("[")
	for i, b := range blockers {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprintf("rel=%s current=%s", b.Rel, b.Current.String()))
		if b.Expected != nil {
			sb.WriteString(fmt.Sprintf(" expected=%s", b.Expected.String()))
		}
		sb.WriteString(fmt.Sprintf(" reason=%s", b.Reason))
	}
	sb.WriteString("]")
	return sb.String()
}

// shrinkDescendantsToParent clamps live descendants into newParent before the
// parent itself shrinks, so the cgroup v1 parent-superset invariant holds at
// each write. The first shrink attempt may use the per-apply children memo for
// speed; after a parent write fails, callers retry with liveChildren=true so a
// newly-created child cannot escape clamping and pin the parent shrink open.
func shrinkDescendantsToParent(ctx context.Context, cg cgroupclient.CgroupClient, relPath string, newParent machine.CPUSet, expected map[string]machine.CPUSet, version cgroupclient.CgroupVersion, res *DAGApplyResult, depth int, cache *applyCache, liveChildren bool) {
	if depth >= maxEnforceDepth {
		return
	}
	children, err := listShrinkChildren(ctx, cg, relPath, cache, liveChildren)
	if err != nil {
		general.InfofV(5, "topo_dag_writer: list children failed during shrink follow, rel=%q err=%v", relPath, err)
		return
	}
	convergeChild := func(childRel string, cpus machine.CPUSet) {
		res.Attempted++
		if err := shrinkNodeConverge(ctx, cg, childRel, cpus, "", expected, version, res, depth+1, cache); err != nil {
			if isCgroupNotFoundError(err) {
				res.Skipped++
				general.InfofV(5, "topo_dag_writer: skip disappeared dynamic cgroup during shrink, rel=%q target=%s err=%v",
					childRel, cpus.String(), err)
				return
			}
			res.Failed++
			general.InfofV(5, "topo_dag_writer: shrink child converge failed, rel=%q err=%v", childRel, err)
			return
		}
		res.Applied++
	}
	for _, name := range children {
		childRel := filepath.Join(relPath, name)
		if exp, ok := expected[childRel]; ok && !exp.IsEmpty() && exp.IsSubsetOf(newParent) {
			convergeChild(childRel, exp)
			continue
		}
		cur, readErr := cg.ReadCPUSet(ctx, childRel)
		if readErr != nil {
			continue
		}
		if version == cgroupclient.CgroupVersionV2 && cur.IsEmpty() {
			shrinkDescendantsToParent(ctx, cg, childRel, newParent, expected, version, res, depth+1, cache, liveChildren)
			continue
		}
		if !newParent.IsEmpty() && isUnderRelPrefix(childRel, cache.kubeRelPrefix) {
			convergeChild(childRel, newParent)
			continue
		}
		if cur.IsSubsetOf(newParent) {
			shrinkDescendantsToParent(ctx, cg, childRel, cur, expected, version, res, depth+1, cache, liveChildren)
			continue
		}
		clamped := cur.Intersection(newParent)
		if clamped.IsEmpty() {
			if version == cgroupclient.CgroupVersionV1 && !newParent.IsEmpty() &&
				!hasLiveTaskInSubtree(ctx, cg, childRel, depth+1, cache, liveChildren) {
				general.InfofV(5, "topo_dag_writer: park disjoint child inside shrinking parent, rel=%q cur=%s newParent=%s",
					childRel, cur.String(), newParent.String())
				convergeChild(childRel, newParent)
				continue
			}
			general.InfofV(5, "topo_dag_writer: shrink follow skipped disjoint child, rel=%q cur=%s newParent=%s", childRel, cur.String(), newParent.String())
			continue
		}
		convergeChild(childRel, clamped)
	}
}

func isUnderRelPrefix(rel, prefix string) bool {
	rel = strings.Trim(rel, "/")
	prefix = strings.Trim(prefix, "/")
	if rel == "" || prefix == "" {
		return false
	}
	return rel != prefix && strings.HasPrefix(rel, prefix+"/")
}

func hasLiveTaskInSubtree(ctx context.Context, cg cgroupclient.CgroupClient, rel string, depth int, cache *applyCache, liveChildren bool) bool {
	if depth >= maxEnforceDepth {
		return false
	}
	for _, file := range []string{"tasks", "cgroup.procs"} {
		raw, err := cg.ReadCgroupFile(ctx, rel, file)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(raw)) != "" {
			return true
		}
	}
	children, err := listShrinkChildren(ctx, cg, rel, cache, liveChildren)
	if err != nil {
		return false
	}
	for _, name := range children {
		if hasLiveTaskInSubtree(ctx, cg, filepath.Join(rel, name), depth+1, cache, liveChildren) {
			return true
		}
	}
	return false
}

func primaryRelPath(dag *TopoDAG) string {
	if dag == nil {
		return ""
	}
	for _, n := range dag.Nodes() {
		if n.Role == TopoNodeRolePrimary {
			return n.Rel
		}
	}
	return ""
}

func desiredTargets(dag *TopoDAG) map[string]machine.CPUSet {
	effective := map[string]machine.CPUSet{}
	for _, n := range dag.Nodes() {
		effective[n.Rel] = n.CPUs
	}
	return effective
}

// computeEffectiveTargets returns, per controlled node, the cpuset that must be
// enforced so the cgroup v1 parent-superset invariant holds while expected kube
// cgroups and reclaim NUMA buckets converge:
//
//	effective(primary) = desired(primary) ∪ protectedPending (allocations with no leaf yet)
//	effective(reclaim parent) includes its reclaim NUMA bucket targets
//
// Existing kube leaves under a primary rel are expected to converge through the
// shrink/expand descendant pass instead of widening the primary effective
// target with inherited full-machine cpusets.
//
// After pending widening, reclaim targets are deducted by the union of primary
// effective targets, reclaim parents are widened to contain NUMA buckets, and
// reclaim NUMA siblings are checked for disjointness.
func computeEffectiveTargets(dag *TopoDAG, allowEmptyTarget bool, protectedPending machine.CPUSet) (map[string]machine.CPUSet, error) {
	effective := desiredTargets(dag)
	for _, n := range dag.Nodes() {
		if n.Role != TopoNodeRolePrimary {
			continue
		}
		// On cgroup v2, an empty cpuset.cpus is a valid explicit target and
		// means the node inherits its effective CPUs from ancestors. Do not widen
		// an intentionally empty target with protected current/pending CPUs; otherwise
		// we would erase the empty-target semantics before expandDescendants has a
		// chance to propagate it.
		if allowEmptyTarget && n.CPUs.IsEmpty() {
			continue
		}
		protectedUnion := machine.NewCPUSet()
		if !protectedPending.IsEmpty() {
			protectedUnion = protectedUnion.Union(protectedPending)
		}
		if protectedUnion.IsEmpty() {
			continue
		}
		effective[n.Rel] = n.CPUs.Union(protectedUnion)
		general.InfofV(5, "topo_dag_writer: widen primary effective target for pending allocations, rel=%q desired=%s pending=%s effective=%s",
			n.Rel, n.CPUs.String(), protectedUnion.String(), effective[n.Rel].String())
	}
	normalizeReclaimTargetsByPrimary(dag, effective)
	normalizeReclaimParentContainsNUMABuckets(dag, effective)
	if err := validateNoPrimaryReclaimOverlap(dag, effective); err != nil {
		return nil, err
	}
	if err := validateReclaimNUMABucketSiblingsDisjoint(dag, effective); err != nil {
		return nil, err
	}
	return effective, nil
}

func normalizeReclaimTargetsByPrimary(dag *TopoDAG, effective map[string]machine.CPUSet) {
	primaryUnion := machine.NewCPUSet()
	for _, n := range dag.Nodes() {
		if n.Role == TopoNodeRolePrimary {
			primaryUnion = primaryUnion.Union(effective[n.Rel])
		}
	}
	if primaryUnion.IsEmpty() {
		return
	}
	for _, n := range dag.Nodes() {
		switch n.Role {
		case TopoNodeRoleReclaim, TopoNodeRoleReclaimNUMABucket, TopoNodeRoleReclaimSibling:
			original := effective[n.Rel]
			deducted := original.Difference(primaryUnion)
			if !deducted.Equals(original) {
				general.InfofV(5, "topo_dag_writer: deduct primary effective cpuset from reclaim target, rel=%q original=%s primary=%s effective=%s",
					n.Rel, original.String(), primaryUnion.String(), deducted.String())
				effective[n.Rel] = deducted
			}
		}
	}
}

func normalizeReclaimParentContainsNUMABuckets(dag *TopoDAG, effective map[string]machine.CPUSet) {
	for _, n := range dag.Nodes() {
		if n.Role != TopoNodeRoleReclaimNUMABucket {
			continue
		}
		parent := reclaimParentForBucket(dag, n)
		if parent == nil {
			continue
		}
		childTarget := effective[n.Rel]
		parentTarget := effective[parent.Rel]
		if childTarget.IsSubsetOf(parentTarget) {
			continue
		}
		widened := parentTarget.Union(childTarget)
		general.InfofV(5, "topo_dag_writer: widen reclaim parent target for numa bucket, parent=%q child=%q parentTarget=%s childTarget=%s effective=%s",
			parent.Rel, n.Rel, parentTarget.String(), childTarget.String(), widened.String())
		effective[parent.Rel] = widened
	}
}

// validateNoPrimaryReclaimOverlap rejects the apply if any primary/non-reclaim
// effective target overlaps a reclaim target. The overlap is reported per rel so
// the operator can see the conflicting partition and the offending cpus.
func validateNoPrimaryReclaimOverlap(dag *TopoDAG, effective map[string]machine.CPUSet) error {
	var reclaims []*TopoNode
	for _, n := range dag.Nodes() {
		switch n.Role {
		case TopoNodeRoleReclaim, TopoNodeRoleReclaimNUMABucket, TopoNodeRoleReclaimSibling:
			reclaims = append(reclaims, n)
		}
	}
	if len(reclaims) == 0 {
		return nil
	}
	for _, n := range dag.Nodes() {
		if n.Role != TopoNodeRolePrimary {
			continue
		}
		primaryTarget := effective[n.Rel]
		for _, r := range reclaims {
			overlap := primaryTarget.Intersection(effective[r.Rel])
			if !overlap.IsEmpty() {
				return fmt.Errorf("ApplyDAGDiff: partition cpuset overlap: primary=%s target=%s reclaim=%s target=%s overlap=%s",
					n.Rel, primaryTarget.String(), r.Rel, effective[r.Rel].String(), overlap.String())
			}
		}
	}
	return nil
}

func validateReclaimNUMABucketSiblingsDisjoint(dag *TopoDAG, effective map[string]machine.CPUSet) error {
	for _, parent := range dag.Nodes() {
		if parent.Role != TopoNodeRoleReclaim {
			continue
		}
		var buckets []*TopoNode
		for _, child := range parent.children {
			if child.Role == TopoNodeRoleReclaimNUMABucket {
				buckets = append(buckets, child)
			}
		}
		for i := range buckets {
			for j := i + 1; j < len(buckets); j++ {
				left := buckets[i]
				right := buckets[j]
				overlap := effective[left.Rel].Intersection(effective[right.Rel])
				if !overlap.IsEmpty() {
					return fmt.Errorf("ApplyDAGDiff: reclaim numa bucket overlap: parent=%s left=%s target=%s right=%s target=%s overlap=%s",
						parent.Rel,
						left.Rel, effective[left.Rel].String(),
						right.Rel, effective[right.Rel].String(),
						overlap.String())
				}
			}
		}
	}
	return nil
}

func allowReclaimNUMABucketDisjointReplacement(dag *TopoDAG, node *TopoNode, target machine.CPUSet, effective map[string]machine.CPUSet) (bool, string) {
	if node == nil || node.Role != TopoNodeRoleReclaimNUMABucket {
		return false, "role_not_allowed"
	}
	parent := reclaimParentForBucket(dag, node)
	if parent == nil {
		return false, "missing_reclaim_parent"
	}
	parentTarget := effective[parent.Rel]
	if !target.IsSubsetOf(parentTarget) {
		return false, fmt.Sprintf("target_not_subset_of_parent parent=%s parentTarget=%s", parent.Rel, parentTarget.String())
	}
	for _, sibling := range parent.children {
		if sibling.Rel == node.Rel || sibling.Role != TopoNodeRoleReclaimNUMABucket {
			continue
		}
		overlap := target.Intersection(effective[sibling.Rel])
		if !overlap.IsEmpty() {
			return false, fmt.Sprintf("sibling_overlap sibling=%s overlap=%s", sibling.Rel, overlap.String())
		}
	}
	return true, "reclaim_numa_bucket_parent_contains_target"
}

func reclaimParentForBucket(dag *TopoDAG, node *TopoNode) *TopoNode {
	if dag == nil || node == nil || node.Role != TopoNodeRoleReclaimNUMABucket {
		return nil
	}
	for _, parent := range dag.Nodes() {
		if parent.Role != TopoNodeRoleReclaim {
			continue
		}
		for _, child := range parent.children {
			if child.Rel == node.Rel {
				return parent
			}
		}
	}
	return nil
}

func expandDescendants(ctx context.Context, cg cgroupclient.CgroupClient, parentRel string, parentTarget machine.CPUSet, parentInExpected bool, controlledRels map[string]struct{}, expected map[string]machine.CPUSet, allowEmptyTarget bool, res *DAGApplyResult, firstErr *error, depth int, cache *applyCache) {
	if depth >= maxEnforceDepth || (parentTarget.IsEmpty() && !allowEmptyTarget) {
		return
	}
	version := cg.Version(ctx)
	children, err := cache.listChildren(ctx, parentRel)
	if err != nil {
		general.InfofV(5, "topo_dag_writer: list children failed during expand descent, rel=%q err=%v", parentRel, err)
		return
	}
	writeAndDescend := func(childRel string, cpus machine.CPUSet, childInExpected bool) {
		res.Attempted++
		if err := shrinkNodeConverge(ctx, cg, childRel, cpus, "", expected, version, res, depth+1, cache); err != nil {
			if isCgroupNotFoundError(err) {
				res.Skipped++
				general.InfofV(5, "topo_dag_writer: skip disappeared dynamic cgroup during expand, rel=%q target=%s err=%v",
					childRel, cpus.String(), err)
				return
			}
			res.Failed++
			if *firstErr == nil {
				*firstErr = err
			}
			// Do NOT recurse: the parent write failed, so the subtree's assumed
			// parent target is not actually in effect. Continuing to write
			// descendants against that target could violate the v1 parent-superset
			// invariant or narrow leaves below their real parent.
			general.InfofV(5, "topo_dag_writer: skip subtree descent after apply failure, rel=%q target=%s err=%v",
				childRel, cpus.String(), err)
			return
		}
		res.Applied++
		expandDescendants(ctx, cg, childRel, cpus, childInExpected, controlledRels, expected, allowEmptyTarget, res, firstErr, depth+1, cache)
	}
	for _, name := range children {
		childRel := filepath.Join(parentRel, name)
		if _, isControlled := controlledRels[childRel]; isControlled {
			continue
		}
		if exp, ok := expected[childRel]; ok {
			if !exp.IsEmpty() || allowEmptyTarget {
				writeAndDescend(childRel, exp, true)
			}
			continue
		}
		// Runtime-managed descendants are also converged by bulkhead. They are
		// not used to widen parent targets with inherited full-machine cpusets;
		// instead this expand/shrink pass recursively propagates the parent
		// target unless a more specific expected target was supplied above.
		if parentInExpected {
			cur, readErr := cg.ReadCPUSet(ctx, childRel)
			if readErr != nil {
				// Under an expected parent we have no ground truth for the
				// child's current cpuset. Writing parentTarget here would
				// blindly widen an unmanaged child (e.g. a live pod leaf) or
				// clobber an inherited v2 empty target. Skip and let the next
				// round retry once the read is healthy again.
				general.InfofV(5, "topo_dag_writer: read cpuset failed under expected parent, skip child, rel=%q err=%v", childRel, readErr)
				res.Skipped++
				continue
			}
			if allowEmptyTarget && cur.IsEmpty() {
				expandDescendants(ctx, cg, childRel, parentTarget, true, controlledRels, expected, allowEmptyTarget, res, firstErr, depth+1, cache)
				continue
			}
			if cur.IsSubsetOf(parentTarget) {
				if !cur.IsEmpty() {
					expandDescendants(ctx, cg, childRel, cur, true, controlledRels, expected, allowEmptyTarget, res, firstErr, depth+1, cache)
				}
				continue
			}
			clamped := cur.Intersection(parentTarget)
			if clamped.IsEmpty() {
				general.InfofV(5, "topo_dag_writer: expand descent skipped disjoint subset-only child, rel=%q cur=%s parent=%s", childRel, cur.String(), parentTarget.String())
				continue
			}
			writeAndDescend(childRel, clamped, true)
			continue
		}
		if allowEmptyTarget {
			if obs, obsErr := cg.ReadCPUSet(ctx, childRel); obsErr == nil && obs.IsEmpty() {
				expandDescendants(ctx, cg, childRel, parentTarget, false, controlledRels, expected, allowEmptyTarget, res, firstErr, depth+1, cache)
				continue
			}
		}
		writeAndDescend(childRel, parentTarget, false)
	}
}

func observedCPUSetDescendantUnion(ctx context.Context, cg cgroupclient.CgroupClient, rel string, depth int, cache *applyCache) machine.CPUSet {
	out := machine.NewCPUSet()
	if depth >= maxEnforceDepth {
		return out
	}
	children, err := cache.listChildren(ctx, rel)
	if err != nil {
		return out
	}
	for _, name := range children {
		childRel := filepath.Join(rel, name)
		if cpus, readErr := cg.ReadCPUSet(ctx, childRel); readErr == nil {
			out = out.Union(cpus)
		}
		out = out.Union(observedCPUSetDescendantUnion(ctx, cg, childRel, depth+1, cache))
	}
	return out
}

// applyCache is a per-ApplyDAGDiff memo that eliminates repeated cgroup tree
// walks within one applyTwoPhase invocation. It must NOT be reused across
// applies.
//
// IMPORTANT - staleness boundary: container cgroups are created out-of-band by
// kubelet/containerd, so the child set of a rel CAN change during a single
// apply (a new pod/container leaf may be mkdir'd between two syscalls). The
// cache therefore only backs code paths where a stale (slightly-too-old)
// children view is SAFE:
//   - effective-target computation only folds in QRM state known pending
//     allocations and does not depend on a complete live child list.
//   - the expand descent is a single forward fail-open pass: missing a child
//     just skips it this round.
//   - the first shrink convergence attempt can use the cached children view as
//     a fast path, because a successful parent write proves no hidden child is
//     violating the target at that moment.
//
// Once a shrink parent write fails, retry and diagnostics MUST fall back to live
// cg.ListChildren: the shrink path then has evidence that the cached child view
// might be incomplete, and it must re-observe a converging tree to catch children
// created after the cache snapshot.
//
// Fields:
//   - children: memoized cg.ListChildren(rel) result, shared among fail-open
//     callers and the first shrink convergence attempt.
//   - expectedDescendantIdx: prefix -> union of every ExpectedCPUSetByRel entry
//     whose rel equals or lives under prefix. Precomputed once so that
//     expectedDescendantUnion becomes an O(1) lookup instead of scanning the
//     entire expected map per call. Independent of live cgroup state.
type applyCache struct {
	cg                    cgroupclient.CgroupClient
	kubeRelPrefix         string
	children              map[string][]string
	expectedDescendantIdx map[string]machine.CPUSet
}

func newApplyCache(cg cgroupclient.CgroupClient, kubeRelPrefix string, expected map[string]machine.CPUSet) *applyCache {
	c := &applyCache{
		cg:            cg,
		kubeRelPrefix: kubeRelPrefix,
		children:      map[string][]string{},
	}
	c.expectedDescendantIdx = buildExpectedDescendantIndex(expected)
	return c
}

// listChildren returns the memoized cg.ListChildren(rel). It caches both
// success and empty results; on error the empty slice is returned but NOT
// cached (so a transient failure can recover on the next call). callers must
// treat the returned slice as read-only.
func (c *applyCache) listChildren(ctx context.Context, rel string) ([]string, error) {
	if v, ok := c.children[rel]; ok {
		return v, nil
	}
	v, err := c.cg.ListChildren(ctx, rel)
	if err != nil {
		return nil, err
	}
	// Defensive copy: the underlying cgroup client may reuse buffers.
	cp := append([]string(nil), v...)
	c.children[rel] = cp
	return cp, nil
}

// expectedDescendantUnion returns the pre-indexed union of every expected leaf
// living at or below rel. It is O(1) after the initial index build.
func (c *applyCache) expectedDescendantUnion(rel string) machine.CPUSet {
	if c == nil {
		return machine.NewCPUSet()
	}
	prefix := strings.Trim(rel, "/")
	if prefix == "" {
		return machine.NewCPUSet()
	}
	if v, ok := c.expectedDescendantIdx[prefix]; ok {
		return v
	}
	return machine.NewCPUSet()
}

// buildExpectedDescendantIndex materializes the prefix -> union map used by
// expectedDescendantUnion. Cost is O(sum(len(rel)/'/')) which is bounded and
// paid once per apply; every downstream call becomes O(1).
func buildExpectedDescendantIndex(expected map[string]machine.CPUSet) map[string]machine.CPUSet {
	idx := map[string]machine.CPUSet{}
	if len(expected) == 0 {
		return idx
	}
	for rel, cpus := range expected {
		trimmed := strings.Trim(rel, "/")
		if trimmed == "" {
			continue
		}
		// Union cpus into every prefix ancestor (inclusive of the rel itself).
		for {
			if existing, ok := idx[trimmed]; ok {
				idx[trimmed] = existing.Union(cpus)
			} else {
				idx[trimmed] = cpus.Clone()
			}
			slash := strings.LastIndex(trimmed, "/")
			if slash < 0 {
				break
			}
			trimmed = trimmed[:slash]
			if trimmed == "" {
				break
			}
		}
	}
	return idx
}
