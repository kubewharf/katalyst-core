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
	"strings"
	"syscall"

	"k8s.io/klog/v2"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const maxEnforceDepth = 8

var errDepthLimitReached = errors.New("bulkhead cgroup depth limit reached")

type ApplyMode string

const (
	ApplyModeNormalAdjustment ApplyMode = "normal_adjustment"
	ApplyModeResetExpandOnly  ApplyMode = "reset_expand_only"
)

type DAGApplyResult struct {
	Attempted         int
	Applied           int
	Skipped           int
	Failed            int
	FullyConverged    bool
	ConvergenceReport ConvergenceReport
}

type DAGApplyInputs struct {
	DAG    *TopoDAG
	Cgroup cgroupclient.CgroupClient
	Mems   string
	// Mode selects the semantic apply path. Empty mode preserves normal
	// adjustment behavior for callers that omit the mode.
	Mode                ApplyMode
	CPUDetails          machine.CPUDetails
	ReservedCPUSet      machine.CPUSet
	ExpectedCPUSetByRel map[string]machine.CPUSet
	// KubeManagedRelPrefix scopes Kubernetes-managed subtree handling to the
	// configured primary rel path. Empty prefix falls back to the DAG primary rel.
	KubeManagedRelPrefix string
	// ProtectedPendingCPUSet is the union of container allocations that already
	// exist in QRM state but whose cgroup leaf has not been created yet (pod
	// admit window). These have no resolvable rel, so the writer folds them into
	// the primary node's effective target to guarantee the primary cgroup never
	// shrinks below an allocation that is about to materialize. Unresolved leaves
	// cannot be written directly, so this ancestor protection preserves cgroup v1
	// parent-superset validity while kubelet/runc create the leaf.
	ProtectedPendingCPUSet machine.CPUSet
	// ProtectedCPUSetByRel records cgroup rels whose current/pending cpuset must
	// stay covered during a short runtime creation window. The writer propagates
	// each protected rel to controlled ancestors, so cgroup v1 parent-superset
	// constraints hold while kubelet/runc create child cgroups.
	ProtectedCPUSetByRel map[string]machine.CPUSet
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

func applyTwoPhase(ctx context.Context, in DAGApplyInputs, res *DAGApplyResult) error {
	dag := in.DAG
	cg := in.Cgroup
	// Normal adjustment needs CPUDetails to construct a topology-safe transfer
	// plan. Validate it before querying cgroups so a transient topology outage
	// cannot partially mutate the hierarchy; this function returns before any
	// cgroup write when CPUDetails is unavailable.
	if in.Mode != ApplyModeResetExpandOnly && len(in.CPUDetails) == 0 {
		return errors.New("ApplyDAGDiff: empty CPUDetails in normal mode")
	}
	kubeRelPrefix := in.KubeManagedRelPrefix
	version := cg.Version(ctx)
	allowEmptyTarget := version == cgroupclient.CgroupVersionV2
	if strings.Trim(kubeRelPrefix, "/") == "" {
		kubeRelPrefix = primaryRelPath(dag)
	}

	cache := newApplyCache(cg, kubeRelPrefix)
	effectiveTargets := desiredTargets(dag)
	// Reset does not depend on normal topology planning. It applies reset targets
	// to controlled and discovered descendants to recover stale cpusets.
	if in.Mode == ApplyModeResetExpandOnly {
		if err := applyResetExpandOnly(ctx, in, effectiveTargets, allowEmptyTarget, res); err != nil {
			return err
		}
		report := verifyResetConvergence(ctx, cg, dag, effectiveTargets)
		res.ConvergenceReport = report
		res.FullyConverged = report.FullyConverged
		return nil
	}

	effectiveTargets, err := computeEffectiveTargets(dag, allowEmptyTarget, in.CPUDetails, in.ProtectedPendingCPUSet, in.ProtectedCPUSetByRel)
	if err != nil {
		return err
	}
	pipeline := newDomainPhasePipeline(dag, cg, effectiveTargets, in.CPUDetails, in.ReservedCPUSet, cache)
	if err := pipeline.executeTransferCycle(ctx, in.Mems, res); err != nil {
		return err
	}
	if err := convergeControlledNodes(ctx, in, effectiveTargets, allowEmptyTarget, res); err != nil {
		return err
	}
	report, err := buildConvergenceReport(ctx, cg, dag, effectiveTargets, in.CPUDetails, in.ReservedCPUSet, allowEmptyTarget, cache)
	if err != nil {
		return err
	}
	res.ConvergenceReport = report
	res.FullyConverged = report.FullyConverged
	return nil
}

func applyResetExpandOnly(ctx context.Context, in DAGApplyInputs, targets map[string]machine.CPUSet, allowEmptyTarget bool, res *DAGApplyResult) error {
	writer := newSafeCPUSetWriterForDAG(ctx, in.Cgroup, in.DAG, targets, in.Mems, res)
	controlled := map[string]struct{}{}
	for _, n := range in.DAG.Nodes() {
		controlled[n.Rel] = struct{}{}
	}
	var firstErr error
	_ = in.DAG.ForEachExpand(func(n *TopoNode) error {
		target := targets[n.Rel]
		if target.IsEmpty() && !allowEmptyTarget {
			res.Skipped++
			return nil
		}
		logApplyNodeTarget("reset", n, target, targets, in.Cgroup, ctx)
		if n.Role == TopoNodeRoleReclaimNUMABucket {
			if err := writer.shrinkParentWithLiveChildUnion(n, target); err != nil && firstErr == nil {
				firstErr = err
			}
		} else {
			if err := writer.growNodeWithParentBridge(n, target); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		writer.propagateResetTarget(n.Rel, target, controlled, in.ExpectedCPUSetByRel, &firstErr, 0)
		return nil
	})
	return firstErr
}

func (w safeCPSetWriter) propagateResetTarget(parentRel string, parentTarget machine.CPUSet, controlled map[string]struct{}, expected map[string]machine.CPUSet, firstErr *error, depth int) {
	if depth >= maxEnforceDepth {
		if w.res != nil {
			w.res.Skipped++
		}
		return
	}
	children, err := w.cg.ListChildren(w.ctx, parentRel)
	if err != nil {
		return
	}
	for _, name := range children {
		childRel := filepath.Join(parentRel, name)
		if _, ok := controlled[childRel]; ok {
			continue
		}
		target, hasExpected := expected[childRel]
		if !hasExpected {
			target = parentTarget
		}
		// Reset recursively applies an expected target to a child when supplied;
		// otherwise the child inherits its parent target. It fails open to restore
		// legacy stale cpuset state without promising expansion in a single pass.
		if err := w.writeDynamicRel(childRel, target, ""); err != nil {
			if *firstErr == nil {
				*firstErr = err
			}
			continue
		}
		w.propagateResetTarget(childRel, target, controlled, expected, firstErr, depth+1)
	}
}

func convergeControlledNodes(ctx context.Context, in DAGApplyInputs, targets map[string]machine.CPUSet, allowEmptyTarget bool, res *DAGApplyResult) error {
	writer := newSafeCPUSetWriterForDAG(ctx, in.Cgroup, in.DAG, targets, in.Mems, res).withCPUDetails(in.CPUDetails)
	var firstErr error
	_ = in.DAG.ForEachShrink(func(n *TopoNode) error {
		target := targets[n.Rel]
		if target.IsEmpty() && !allowEmptyTarget {
			res.Skipped++
			return nil
		}
		observed, err := in.Cgroup.ReadCPUSet(ctx, n.Rel)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("read cpuset before shrink, rel=%s: %w", n.Rel, err)
			}
			return nil
		}
		if !observed.IsSubsetOf(target) {
			logApplyNodeTarget("converge_shrink", n, target, targets, in.Cgroup, ctx)
			if err := writer.shrinkParentWithLiveChildUnion(n, target); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return nil
	})
	if firstErr != nil {
		return firstErr
	}
	_ = in.DAG.ForEachExpand(func(n *TopoNode) error {
		target := targets[n.Rel]
		if target.IsEmpty() && !allowEmptyTarget {
			res.Skipped++
			return nil
		}
		observed, err := in.Cgroup.ReadCPUSet(ctx, n.Rel)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("read cpuset before grow, rel=%s: %w", n.Rel, err)
			}
			return nil
		}
		if !target.IsSubsetOf(observed) {
			logApplyNodeTarget("converge_expand", n, target, targets, in.Cgroup, ctx)
			if err := writer.growNodeWithParentBridge(n, target); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return nil
	})
	return firstErr
}

func logApplyNodeTarget(phase string, node *TopoNode, target machine.CPUSet, targetByRel map[string]machine.CPUSet, cg cgroupclient.CgroupClient, ctx context.Context) {
	if node == nil || !klog.V(4).Enabled() {
		return
	}
	current := "<read_error>"
	if cur, err := cg.ReadCPUSet(ctx, node.Rel); err == nil {
		current = cur.String()
	}
	targetByRelValue := "<missing>"
	if value, ok := targetByRel[node.Rel]; ok {
		targetByRelValue = value.String()
	}
	parentRel := ""
	if parent := parentNodeOf(node); parent != nil {
		parentRel = parent.Rel
	}
	general.InfofV(4, "topo_dag_writer: apply_node phase=%s rel=%q role=%v parent=%q current=%s target=%s targetByRel=%s nodeCPUs=%s mems=%q metadata=%v",
		phase, node.Rel, node.Role, parentRel, current, target.String(), targetByRelValue, node.CPUs.String(), memsForNode(node, ""), node.Metadata)
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
		current := "<read_error>"
		if cur, readErr := cg.ReadCPUSet(ctx, rel); readErr == nil {
			current = cur.String()
		}
		general.InfofV(4, "topo_dag_writer: cpuset_write failed rel=%q current=%s target=%s mems=%q err=%v", rel, current, cpus.String(), mems, err)
		return fmt.Errorf("apply cpuset.cpus=%s @ %s: %w", cpus.String(), rel, err)
	}
	general.InfofV(6, "topo_dag_writer: cpuset_write done rel=%q target=%s mems=%q", rel, cpus.String(), mems)
	return nil
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

func primaryRelPath(dag *TopoDAG) string {
	if dag == nil {
		return ""
	}
	for _, n := range dag.Nodes() {
		if isPrimaryRole(n.Role) {
			return n.Rel
		}
	}
	return ""
}

// applyCache is a per-ApplyDAGDiff memo that eliminates repeated cgroup tree
// walks within one applyTwoPhase invocation. It must NOT be reused across
// applies.
//
// The cache is only used for snapshot/diagnostic reads. Safe writer shrink paths
// intentionally re-list live children before final parent shrink so new runtime
// descendants cannot escape convergence.
type applyCache struct {
	cg            cgroupclient.CgroupClient
	kubeRelPrefix string
	children      map[string][]string
}

func newApplyCache(cg cgroupclient.CgroupClient, kubeRelPrefix string) *applyCache {
	return &applyCache{
		cg:            cg,
		kubeRelPrefix: kubeRelPrefix,
		children:      map[string][]string{},
	}
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
