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
	"strings"

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
	// ProtectUnmanagedKubePodLeaf is the legacy switch for Kubernetes-managed
	// subtree safety. When true, it keeps expected kube container cgroups and
	// admit-pending allocations from being under-cut by bulkhead's DAG apply.
	// Concretely it does three things:
	//
	//  1. Widens every primary node's effective target to include the union of
	//     the current cpuset of kube cgroups present in ExpectedCPUSetByRel
	//     found under it (see collectProtectedLeafCPUSet). This preserves the
	//     cgroup v1 parent-superset invariant when a parent must be written
	//     before the child is updated to its resolved allocation.
	//  2. Additionally folds ProtectedPendingCPUSet (allocations whose cgroup
	//     leaf has not been created yet) into the same widened primary target,
	//     so the primary never shrinks below an allocation about to materialize.
	//  3. During the expand descent, refuses to propagate the parent pool target
	//     onto pod/container leaves that are not present in ExpectedCPUSetByRel
	//     (writing a transient pool cpuset onto a live container leaf can leave
	//     it disjoint from the parent after the pool shrinks). Intermediate
	//     nodes containing expected container leaves are still expanded safely
	//     via current ∪ observedDescendants ∪ expectedDescendants.
	//
	// After widening the primary, reclaim targets are normalized by removing the
	// primary effective CPUs from them, then reclaim parents are widened to cover
	// their NUMA buckets. This keeps the partitions disjoint while preserving the
	// cgroup v1 parent-superset invariant. Leaves that DO appear in
	// ExpectedCPUSetByRel are still written to their resolved allocation.
	ProtectUnmanagedKubePodLeaf bool
	// KubeManagedRelPrefix scopes the protection above to rels under this prefix
	// (typically BulkheadPrimaryRelPath, e.g. "kubepods"). Empty prefix means the
	// protection is not scoped and applies to any pod-looking rel. Passing the
	// configured primary rel path avoids hard-coding "kubepods/" in the writer.
	KubeManagedRelPrefix string
	// ProtectedPendingCPUSet is the union of container allocations that already
	// exist in QRM state but whose cgroup leaf has not been created yet (pod
	// admit window). These have no resolvable rel, so the writer folds them into
	// the primary node's effective target to guarantee the primary cgroup never
	// shrinks below an allocation that is about to materialize. It is only used
	// when ProtectUnmanagedKubePodLeaf is true.
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
	err := applyTwoPhase(ctx, in.DAG, in.Cgroup, in.Mems, in.SkipObservedRead, in.ExpectedCPUSetByRel, in.ProtectUnmanagedKubePodLeaf, in.KubeManagedRelPrefix, in.ProtectedPendingCPUSet, &res)
	return res, err
}

type nodeDiff struct {
	grow         bool
	shrink       bool
	target       machine.CPUSet
	shrinkTarget machine.CPUSet
}

func applyTwoPhase(ctx context.Context, dag *TopoDAG, cg cgroupclient.CgroupClient, mems string, skipRead bool, expected map[string]machine.CPUSet, protectKubeLeaf bool, kubeRelPrefix string, protectedPending machine.CPUSet, res *DAGApplyResult) error {
	version := cg.Version(ctx)
	allowEmptyTarget := version == cgroupclient.CgroupVersionV2
	diffs := map[string]nodeDiff{}
	controlledRels := map[string]struct{}{}
	for _, n := range dag.Nodes() {
		controlledRels[n.Rel] = struct{}{}
	}

	// A single applyCache is shared by computeEffectiveTargets,
	// collectProtectedLeafCPUSet and expandDescendants so that every
	// (rel -> children), (rel -> kubeInSubtree), (rel -> expectedDescendantUnion)
	// question is answered at most once per apply. Without this cache each of
	// those helpers used to walk the same subtree independently, so a shared
	// ancestor could be traversed O(k) times where k is the number of protected
	// leaves under it.
	cache := newApplyCache(cg, kubeRelPrefix, expected)

	// Build the effective target for every controlled node before touching any
	// cgroup: effective = desired ∪ (current cpuset of every expected kube rel in
	// its subtree) [∪ pending allocations for the primary]. This guarantees the
	// cgroup v1 parent-superset invariant when a parent has to be written before
	// expected children converge. Reclaim targets are normalized against widened
	// primary targets before applying, so a boundary CPU temporarily required by
	// the primary is removed from reclaim instead of causing a partition conflict.
	effectiveTargets, err := computeEffectiveTargets(ctx, cg, dag, expected, controlledRels, allowEmptyTarget, protectKubeLeaf, kubeRelPrefix, protectedPending, cache)
	if err != nil {
		return err
	}

	for _, n := range dag.Nodes() {
		target := effectiveTargets[n.Rel]
		if target.IsEmpty() && !allowEmptyTarget {
			res.Skipped++
			continue
		}
		var observed machine.CPUSet
		observedKnown := false
		if !skipRead {
			if cs, readErr := cg.ReadCPUSet(ctx, n.Rel); readErr == nil {
				observed = cs
				observedKnown = true
			}
		}
		if observedKnown && observed.Equals(target) {
			res.Skipped++
			continue
		}
		d := nodeDiff{target: target}
		if skipRead || !observedKnown || observed.IsEmpty() {
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

	var firstErr error
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
		if err := applyCPUSet(ctx, cg, n.Rel, d.shrinkTarget, memsForNode(n, mems)); err == nil {
			res.Applied++
			return nil
		}
		if err := shrinkNodeConverge(ctx, cg, n.Rel, d.shrinkTarget, memsForNode(n, mems), expected, version, res, 0, cache); err != nil {
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
			res.Attempted++
			if err := applyCPUSet(ctx, cg, n.Rel, d.target, memsForNode(n, mems)); err != nil {
				res.Failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("apply cpuset.cpus=%s @ %s: %w", d.target.String(), n.Rel, err)
				}
				// The node's own grow write failed, so its cpuset is still at the
				// smaller observed value. Descending now would write children to
				// the (larger) effective target and violate the cgroup v1
				// parent-superset invariant, mirroring the fail-fast guard in
				// writeAndDescend. Skip this subtree; the next round retries.
				general.InfofV(5, "topo_dag_writer: skip expand descent after node grow failure, rel=%q target=%s", n.Rel, d.target.String())
				return nil
			}
			res.Applied++
		}
		if !effTarget.IsEmpty() || allowEmptyTarget {
			expandDescendants(ctx, cg, n.Rel, effTarget, false, controlledRels, expected, allowEmptyTarget, protectKubeLeaf, kubeRelPrefix, res, &firstErr, 0, cache)
		}
		return nil
	})
	return firstErr
}

func memsForNode(n *TopoNode, defaultMems string) string {
	if n != nil && n.Mems != "" {
		return n.Mems
	}
	return defaultMems
}

func applyCPUSet(ctx context.Context, cg cgroupclient.CgroupClient, rel string, cpus machine.CPUSet, mems string) error {
	data := &cgcommon.CPUSetData{CPUs: cpus.String()}
	if mems != "" {
		data.Mems = mems
	}
	if err := cg.ApplyCPUSet(ctx, rel, data); err != nil {
		return fmt.Errorf("apply cpuset.cpus=%s @ %s: %w", cpus.String(), rel, err)
	}
	return nil
}

func shrinkNodeConverge(ctx context.Context, cg cgroupclient.CgroupClient, relPath string, newSelf machine.CPUSet, mems string, expected map[string]machine.CPUSet, version cgroupclient.CgroupVersion, res *DAGApplyResult, depth int, cache *applyCache) error {
	if depth >= maxEnforceDepth {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < maxShrinkRetries; attempt++ {
		liveChildren := attempt > 0
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
		if cur.IsSubsetOf(newParent) {
			shrinkDescendantsToParent(ctx, cg, childRel, cur, expected, version, res, depth+1, cache, liveChildren)
			continue
		}
		clamped := cur.Intersection(newParent)
		if clamped.IsEmpty() {
			general.InfofV(5, "topo_dag_writer: shrink follow skipped disjoint child, rel=%q cur=%s newParent=%s", childRel, cur.String(), newParent.String())
			continue
		}
		convergeChild(childRel, clamped)
	}
}

// computeEffectiveTargets returns, per controlled node, the cpuset that must be
// enforced so the cgroup v1 parent-superset invariant holds while expected kube
// cgroups and reclaim NUMA buckets converge:
//
//	effective(primary) = desired(primary) ∪ expectedKubeCurrentCPUSet(primary)
//	effective(primary) additionally ∪ protectedPending (allocations with no leaf yet)
//	effective(reclaim parent) includes its reclaim NUMA bucket targets
//
// expectedKubeCurrentCPUSet(rel) is the union of current cpusets for kube rels
// that are present in ExpectedCPUSetByRel. Unrelated pod-scoped cgroups are not
// protected because their current cpuset may be an inherited full-machine value.
//
// After widening, reclaim targets are deducted by the union of primary effective
// targets, reclaim parents are widened to contain NUMA buckets, and reclaim NUMA
// siblings are checked for disjointness.
func computeEffectiveTargets(ctx context.Context, cg cgroupclient.CgroupClient, dag *TopoDAG, expected map[string]machine.CPUSet, controlledRels map[string]struct{}, allowEmptyTarget bool, protectKubeLeaf bool, kubeRelPrefix string, protectedPending machine.CPUSet, cache *applyCache) (map[string]machine.CPUSet, error) {
	effective := map[string]machine.CPUSet{}
	for _, n := range dag.Nodes() {
		effective[n.Rel] = n.CPUs
	}
	if !protectKubeLeaf {
		return effective, nil
	}
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
		protectedUnion := collectProtectedLeafCPUSet(ctx, cg, n.Rel, expected, controlledRels, kubeRelPrefix, 0, cache)
		if !protectedPending.IsEmpty() {
			protectedUnion = protectedUnion.Union(protectedPending)
		}
		if protectedUnion.IsEmpty() {
			continue
		}
		effective[n.Rel] = n.CPUs.Union(protectedUnion)
		general.InfofV(5, "topo_dag_writer: widen primary effective target for protected leaves, rel=%q desired=%s protected=%s effective=%s",
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

// collectProtectedLeafCPUSet walks rel's subtree and unions the current cpuset
// only for kube cgroups present in expected. This protects parent shrink from
// racing ahead of the child cgroup update, without preserving unrelated
// pod-scoped cgroups whose current cpuset may be an inherited/default
// full-machine value.
func collectProtectedLeafCPUSet(ctx context.Context, cg cgroupclient.CgroupClient, rel string, expected map[string]machine.CPUSet, controlledRels map[string]struct{}, kubeRelPrefix string, depth int, cache *applyCache) machine.CPUSet {
	out := machine.NewCPUSet()
	if depth >= maxEnforceDepth {
		return out
	}
	children, err := cache.listChildren(ctx, rel)
	if err != nil {
		general.InfofV(5, "topo_dag_writer: list children failed during expected kube current collection, rel=%q err=%v", rel, err)
		return out
	}
	for _, name := range children {
		childRel := filepath.Join(rel, name)
		if _, isControlled := controlledRels[childRel]; isControlled {
			continue
		}
		if _, ok := expected[childRel]; ok {
			if isKubeManagedPodRel(childRel, kubeRelPrefix) {
				cur, readErr := cg.ReadCPUSet(ctx, childRel)
				switch {
				case readErr != nil:
					general.InfofV(5, "topo_dag_writer: read cpuset failed for expected kube rel protection, widen skipped, rel=%q err=%v", childRel, readErr)
				case !cur.IsEmpty():
					out = out.Union(cur)
				}
			}
		}
		if cache.expectedDescendantUnion(childRel).IsEmpty() {
			continue
		}
		out = out.Union(collectProtectedLeafCPUSet(ctx, cg, childRel, expected, controlledRels, kubeRelPrefix, depth+1, cache))
	}
	return out
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

func expandDescendants(ctx context.Context, cg cgroupclient.CgroupClient, parentRel string, parentTarget machine.CPUSet, parentInExpected bool, controlledRels map[string]struct{}, expected map[string]machine.CPUSet, allowEmptyTarget bool, protectKubeLeaf bool, kubeRelPrefix string, res *DAGApplyResult, firstErr *error, depth int, cache *applyCache) {
	if depth >= maxEnforceDepth || (parentTarget.IsEmpty() && !allowEmptyTarget) {
		return
	}
	children, err := cache.listChildren(ctx, parentRel)
	if err != nil {
		general.InfofV(5, "topo_dag_writer: list children failed during expand descent, rel=%q err=%v", parentRel, err)
		return
	}
	writeAndDescend := func(childRel string, cpus machine.CPUSet, childInExpected bool) {
		res.Attempted++
		if err := applyCPUSet(ctx, cg, childRel, cpus, ""); err != nil {
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
		expandDescendants(ctx, cg, childRel, cpus, childInExpected, controlledRels, expected, allowEmptyTarget, protectKubeLeaf, kubeRelPrefix, res, firstErr, depth+1, cache)
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
		// Leave Kubernetes-managed subtrees that are not present in the expected
		// map untouched: never propagate the parent pool target onto unrelated
		// pod-scoped cgroups. Pod parent cgroups are intermediate nodes, and
		// sandbox/pause/reclaim leaves are not protected by primary widening.
		// Also leave non-pod intermediate nodes untouched when they contain an
		// unresolved pod-scoped descendant; otherwise writing that intermediate
		// node can fail or narrow a parent of unrelated pod-scoped leaves.
		// If the protected subtree contains expected container leaves, safely
		// expand each intermediate node to current ∪ expectedDescendants so the
		// expected leaves can still expand without narrowing unrelated pod leaves.
		if protectKubeLeaf && !parentTarget.IsEmpty() && cache.hasKubeManagedPodInSubtree(ctx, childRel, depth+1) {
			res.Skipped++
			if expUnion := cache.expectedDescendantUnion(childRel); !expUnion.IsEmpty() {
				cur, readErr := cg.ReadCPUSet(ctx, childRel)
				if readErr == nil {
					observedDescendants := observedCPUSetDescendantUnion(ctx, cg, childRel, depth+1, cache)
					safeTarget := cur.Union(observedDescendants).Union(expUnion)
					if safeTarget.IsSubsetOf(parentTarget) {
						general.InfofV(5, "topo_dag_writer: safely expand protected kube subtree intermediate, rel=%q cur=%s observedDescendants=%s expectedDescendants=%s target=%s",
							childRel, cur.String(), observedDescendants.String(), expUnion.String(), safeTarget.String())
						writeAndDescend(childRel, safeTarget, true)
						continue
					}
					if expUnion.IsSubsetOf(cur) && !cur.IsEmpty() {
						general.InfofV(5, "topo_dag_writer: descend protected kube subtree with current cpuset, rel=%q cur=%s expectedDescendants=%s parentTarget=%s",
							childRel, cur.String(), expUnion.String(), parentTarget.String())
						expandDescendants(ctx, cg, childRel, cur, true, controlledRels, expected, allowEmptyTarget, protectKubeLeaf, kubeRelPrefix, res, firstErr, depth+1, cache)
						continue
					}
				}
			}
			general.InfofV(5, "topo_dag_writer: skip unrelated kube subtree during expand, rel=%q parentTarget=%s", childRel, parentTarget.String())
			continue
		}
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
				expandDescendants(ctx, cg, childRel, parentTarget, true, controlledRels, expected, allowEmptyTarget, protectKubeLeaf, kubeRelPrefix, res, firstErr, depth+1, cache)
				continue
			}
			if cur.IsSubsetOf(parentTarget) {
				if !cur.IsEmpty() {
					expandDescendants(ctx, cg, childRel, cur, true, controlledRels, expected, allowEmptyTarget, protectKubeLeaf, kubeRelPrefix, res, firstErr, depth+1, cache)
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
				expandDescendants(ctx, cg, childRel, parentTarget, false, controlledRels, expected, allowEmptyTarget, protectKubeLeaf, kubeRelPrefix, res, firstErr, depth+1, cache)
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

// isKubeManagedPodRel reports whether rel points at a Kubernetes pod-scoped
// cgroup (a "pod<uid>" segment, or anything beneath it such as a container
// leaf) under the given managed prefix. These nodes are owned by
// kubelet/containerd/runc rather than bulkhead. When kubeRelPrefix is non-empty
// the rel must live under it (e.g. "kubepods"); an empty prefix disables the
// scoping and only relies on the pod-segment heuristic.
func isKubeManagedPodRel(rel, kubeRelPrefix string) bool {
	rel = strings.Trim(rel, "/")
	if rel == "" {
		return false
	}
	prefix := strings.Trim(kubeRelPrefix, "/")
	if prefix != "" {
		if rel != prefix && !strings.HasPrefix(rel, prefix+"/") {
			return false
		}
	}
	for _, part := range strings.Split(rel, "/") {
		if isKubePodSegment(part) {
			return true
		}
	}
	return false
}

// isKubePodSegment matches a cgroup path segment that encodes a Kubernetes pod
// UID. Kubelet emits "pod<uid>" for both cgroupfs and systemd layouts; we
// accept the "pod" prefix followed by a UID-like body (hex digits, dashes,
// underscores). Tightening this to require at least one dash/hex-digit was
// considered but skipped to preserve compatibility with all kubelet layouts.
func isKubePodSegment(part string) bool {
	const podPrefix = "pod"
	if len(part) <= len(podPrefix) || !strings.HasPrefix(part, podPrefix) {
		return false
	}
	body := part[len(podPrefix):]
	for _, r := range body {
		if r == '-' || r == '_' ||
			(r >= '0' && r <= '9') ||
			(r >= 'a' && r <= 'f') ||
			(r >= 'A' && r <= 'F') {
			continue
		}
		return false
	}
	return true
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
//   - effective-target widening (collectProtectedLeafCPUSet) is fail-open:
//     missing a just-created leaf only means the primary is not widened to
//     cover it this round; the pending-cpuset union and the next round catch
//     it, and no invariant is broken.
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
//   - kubeInSubtree: memoized hasKubeManagedPodInSubtree(rel). The predicate
//     only depends on rel string shape + subtree topology.
//   - expectedDescendantIdx: prefix -> union of every ExpectedCPUSetByRel entry
//     whose rel equals or lives under prefix. Precomputed once so that
//     expectedDescendantUnion becomes an O(1) lookup instead of scanning the
//     entire expected map per call. Independent of live cgroup state.
type applyCache struct {
	cg                    cgroupclient.CgroupClient
	kubeRelPrefix         string
	children              map[string][]string
	kubeInSubtree         map[string]bool
	expectedDescendantIdx map[string]machine.CPUSet
}

func newApplyCache(cg cgroupclient.CgroupClient, kubeRelPrefix string, expected map[string]machine.CPUSet) *applyCache {
	c := &applyCache{
		cg:            cg,
		kubeRelPrefix: kubeRelPrefix,
		children:      map[string][]string{},
		kubeInSubtree: map[string]bool{},
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

// hasKubeManagedPodInSubtree returns whether rel or any descendant is a
// Kubernetes-managed pod-scoped cgroup. Results are memoized per rel; children
// are resolved through listChildren so the underlying cg.ListChildren is also
// shared with other callers.
func (c *applyCache) hasKubeManagedPodInSubtree(ctx context.Context, rel string, depth int) bool {
	if v, ok := c.kubeInSubtree[rel]; ok {
		return v
	}
	if isKubeManagedPodRel(rel, c.kubeRelPrefix) {
		c.kubeInSubtree[rel] = true
		return true
	}
	if depth >= maxEnforceDepth {
		return false
	}
	children, err := c.listChildren(ctx, rel)
	if err != nil {
		return false
	}
	for _, name := range children {
		childRel := filepath.Join(rel, name)
		if c.hasKubeManagedPodInSubtree(ctx, childRel, depth+1) {
			c.kubeInSubtree[rel] = true
			return true
		}
	}
	c.kubeInSubtree[rel] = false
	return false
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
