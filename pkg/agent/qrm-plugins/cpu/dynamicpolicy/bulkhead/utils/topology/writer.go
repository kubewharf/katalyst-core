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
}

func ApplyDAGDiff(ctx context.Context, in DAGApplyInputs) (DAGApplyResult, error) {
	res := DAGApplyResult{}
	if in.DAG == nil {
		return res, errors.New("ApplyDAGDiff: nil DAG")
	}
	if in.Cgroup == nil {
		return res, errors.New("ApplyDAGDiff: nil Cgroup client")
	}
	err := applyTwoPhase(ctx, in.DAG, in.Cgroup, in.Mems, in.SkipObservedRead, in.ExpectedCPUSetByRel, &res)
	return res, err
}

type nodeDiff struct {
	grow         bool
	shrink       bool
	target       machine.CPUSet
	shrinkTarget machine.CPUSet
}

func applyTwoPhase(ctx context.Context, dag *TopoDAG, cg cgroupclient.CgroupClient, mems string, skipRead bool, expected map[string]machine.CPUSet, res *DAGApplyResult) error {
	version := cg.Version(ctx)
	allowEmptyTarget := version == cgroupclient.CgroupVersionV2
	diffs := map[string]nodeDiff{}
	controlledRels := map[string]struct{}{}
	for _, n := range dag.Nodes() {
		controlledRels[n.Rel] = struct{}{}
		target := n.CPUs
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
					return fmt.Errorf("ApplyDAGDiff: disjoint cpuset change @ %s observed=%s target=%s", n.Rel, observed.String(), target.String())
				}
				d.shrinkTarget = intersection
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
		if err := shrinkNodeConverge(ctx, cg, n.Rel, d.shrinkTarget, memsForNode(n, mems), expected, version, res, 0); err != nil {
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
		if d, ok := diffs[n.Rel]; ok && d.grow {
			res.Attempted++
			if err := applyCPUSet(ctx, cg, n.Rel, d.target, memsForNode(n, mems)); err != nil {
				res.Failed++
				if firstErr == nil {
					firstErr = fmt.Errorf("apply cpuset.cpus=%s @ %s: %w", d.target.String(), n.Rel, err)
				}
			} else {
				res.Applied++
			}
		}
		if !n.CPUs.IsEmpty() || allowEmptyTarget {
			expandDescendants(ctx, cg, n.Rel, n.CPUs, false, controlledRels, expected, allowEmptyTarget, res, &firstErr, 0)
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

func shrinkNodeConverge(ctx context.Context, cg cgroupclient.CgroupClient, relPath string, newSelf machine.CPUSet, mems string, expected map[string]machine.CPUSet, version cgroupclient.CgroupVersion, res *DAGApplyResult, depth int) error {
	if depth >= maxEnforceDepth {
		return nil
	}
	var lastErr error
	for attempt := 0; attempt < maxShrinkRetries; attempt++ {
		shrinkDescendantsToParent(ctx, cg, relPath, newSelf, expected, version, res, depth+1)
		err := applyCPUSet(ctx, cg, relPath, newSelf, mems)
		if err == nil {
			return nil
		}
		lastErr = err
	}
	general.InfofV(4, "topo_dag_writer: shrink converge exhausted, rel=%q newSelf=%s err=%v", relPath, newSelf.String(), lastErr)
	return fmt.Errorf("shrink converge exhausted @ %s: %w", relPath, lastErr)
}

func shrinkDescendantsToParent(ctx context.Context, cg cgroupclient.CgroupClient, relPath string, newParent machine.CPUSet, expected map[string]machine.CPUSet, version cgroupclient.CgroupVersion, res *DAGApplyResult, depth int) {
	if depth >= maxEnforceDepth {
		return
	}
	children, err := cg.ListChildren(ctx, relPath)
	if err != nil {
		general.InfofV(5, "topo_dag_writer: list children failed during shrink follow, rel=%q err=%v", relPath, err)
		return
	}
	convergeChild := func(childRel string, cpus machine.CPUSet) {
		res.Attempted++
		if err := shrinkNodeConverge(ctx, cg, childRel, cpus, "", expected, version, res, depth+1); err != nil {
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
			shrinkDescendantsToParent(ctx, cg, childRel, newParent, expected, version, res, depth+1)
			continue
		}
		if cur.IsSubsetOf(newParent) {
			shrinkDescendantsToParent(ctx, cg, childRel, cur, expected, version, res, depth+1)
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

func expandDescendants(ctx context.Context, cg cgroupclient.CgroupClient, parentRel string, parentTarget machine.CPUSet, parentInExpected bool, controlledRels map[string]struct{}, expected map[string]machine.CPUSet, allowEmptyTarget bool, res *DAGApplyResult, firstErr *error, depth int) {
	if depth >= maxEnforceDepth || (parentTarget.IsEmpty() && !allowEmptyTarget) {
		return
	}
	children, err := cg.ListChildren(ctx, parentRel)
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
		} else {
			res.Applied++
		}
		expandDescendants(ctx, cg, childRel, cpus, childInExpected, controlledRels, expected, allowEmptyTarget, res, firstErr, depth+1)
	}
	for _, name := range children {
		childRel := filepath.Join(parentRel, name)
		if _, isControlled := controlledRels[childRel]; isControlled {
			continue
		}
		if exp, ok := expected[childRel]; ok {
			if !exp.IsEmpty() {
				writeAndDescend(childRel, exp, true)
			}
			continue
		}
		if parentInExpected {
			cur, _ := cg.ReadCPUSet(ctx, childRel)
			if allowEmptyTarget && cur.IsEmpty() {
				expandDescendants(ctx, cg, childRel, parentTarget, true, controlledRels, expected, allowEmptyTarget, res, firstErr, depth+1)
				continue
			}
			if cur.IsSubsetOf(parentTarget) {
				if !cur.IsEmpty() {
					expandDescendants(ctx, cg, childRel, cur, true, controlledRels, expected, allowEmptyTarget, res, firstErr, depth+1)
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
				expandDescendants(ctx, cg, childRel, parentTarget, false, controlledRels, expected, allowEmptyTarget, res, firstErr, depth+1)
				continue
			}
		}
		writeAndDescend(childRel, parentTarget, false)
	}
}
