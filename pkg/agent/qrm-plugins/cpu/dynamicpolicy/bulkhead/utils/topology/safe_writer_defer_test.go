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
	"strings"
	"syscall"
	"testing"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type disappearingReclaimDescendantCgroup struct {
	*topologyFakeCgroup

	disappearingRel string
	disappeared     bool
}

func (f *disappearingReclaimDescendantCgroup) ListChildren(ctx context.Context, rel string) ([]string, error) {
	if rel == f.disappearingRel && !f.disappeared {
		f.disappeared = true
		parentRel := "kubesandbox/reclaimed-1"
		f.children[parentRel] = nil
		delete(f.cpus, rel)
		return nil, syscall.ENOENT
	}
	return f.topologyFakeCgroup.ListChildren(ctx, rel)
}

// singleParentNode builds a one-node DAG and returns the parent TopoNode, used
// by the Fix-3 deferred-convergence tests.
func singleParentNode(t *testing.T, cpus machine.CPUSet) *TopoNode {
	t.Helper()
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "parent", Role: TopoNodeRolePrimary, CPUs: cpus},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	return dag.index["parent"]
}

// TestIsDeferConvergenceError verifies the exported sentinel classifier only
// matches the deferred-convergence sentinel (directly or wrapped) and never a
// plain EBUSY or a nil error. The admission path relies on this to tell a
// transient topology lag apart from a real allocation failure.
func TestIsDeferConvergenceError(t *testing.T) {
	t.Parallel()

	if !IsDeferConvergenceError(errDeferConvergence) {
		t.Fatalf("IsDeferConvergenceError(errDeferConvergence) = false, want true")
	}
	if !IsDeferConvergenceError(fmt.Errorf("wrap: %w", errDeferConvergence)) {
		t.Fatalf("IsDeferConvergenceError(wrapped) = false, want true")
	}
	if IsDeferConvergenceError(syscall.EBUSY) {
		t.Fatalf("IsDeferConvergenceError(EBUSY) = true, want false")
	}
	if IsDeferConvergenceError(nil) {
		t.Fatalf("IsDeferConvergenceError(nil) = true, want false")
	}
}

// TestParentSupersetHeld verifies the safety precondition: it is true only when
// the parent's current cpuset already covers the union of every live child.
func TestParentSupersetHeld(t *testing.T) {
	t.Parallel()

	node := singleParentNode(t, machine.NewCPUSet(0, 1))

	// parent covers the live child union => held.
	cg := newTopologyFakeCgroup()
	cg.cpus["parent"] = machine.NewCPUSet(0, 1)
	cg.cpus["parent/child"] = machine.NewCPUSet(0)
	cg.children["parent"] = []string{"child"}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &DAGApplyResult{})
	if !writer.parentSupersetHeld(node) {
		t.Fatalf("parentSupersetHeld = false, want true when parent covers child union")
	}

	// parent misses a child cpu => not held.
	cg2 := newTopologyFakeCgroup()
	cg2.cpus["parent"] = machine.NewCPUSet(0)
	cg2.cpus["parent/child"] = machine.NewCPUSet(0, 1)
	cg2.children["parent"] = []string{"child"}
	writer2 := newSafeCPUSetWriter(context.Background(), cg2, "0", &DAGApplyResult{})
	if writer2.parentSupersetHeld(node) {
		t.Fatalf("parentSupersetHeld = true, want false when child escapes parent")
	}
}

// TestFinalParentShrinkDefersOnPersistentEBUSY verifies that when the final
// narrowing keeps hitting EBUSY but the parent still covers every live child,
// the write is deferred (errDeferConvergence) instead of failing hard.
func TestFinalParentShrinkDefersOnPersistentEBUSY(t *testing.T) {
	t.Parallel()

	node := singleParentNode(t, machine.NewCPUSet(0, 1))
	cg := newTopologyFakeCgroup()
	// parent stays a valid superset of the live child; only the final narrow fails.
	cg.cpus["parent"] = machine.NewCPUSet(0, 1)
	cg.cpus["parent/child"] = machine.NewCPUSet(0)
	cg.children["parent"] = []string{"child"}
	cg.applyErr["parent"] = syscall.EBUSY
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &res)

	err := writer.finalParentShrink(node, machine.NewCPUSet(0, 1))
	if !IsDeferConvergenceError(err) {
		t.Fatalf("finalParentShrink error = %v, want deferred-convergence sentinel", err)
	}
	// The EBUSY attempts are bounded by the retry budget.
	if res.Failed != maxSafeCPUSetWriteAttempts {
		t.Fatalf("res.Failed = %d, want %d bounded EBUSY attempts", res.Failed, maxSafeCPUSetWriteAttempts)
	}
}

func TestFinalParentShrinkDefersOnTextWrappedEBUSY(t *testing.T) {
	t.Parallel()

	node := singleParentNode(t, machine.NewCPUSet(0, 1))
	cg := newTopologyFakeCgroup()
	cg.cpus["parent"] = machine.NewCPUSet(0, 1)
	cg.cpus["parent/child"] = machine.NewCPUSet(0)
	cg.children["parent"] = []string{"child"}
	cg.applyErr["parent"] = errors.New(`write /sys/fs/cgroup/cpuset/parent/cpuset.cpus: device or resource busy`)
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &res)

	err := writer.finalParentShrink(node, machine.NewCPUSet(0, 1))
	if !IsDeferConvergenceError(err) {
		t.Fatalf("finalParentShrink error = %v, want deferred-convergence sentinel for text-wrapped EBUSY", err)
	}
	if res.Failed != maxSafeCPUSetWriteAttempts {
		t.Fatalf("res.Failed = %d, want %d bounded EBUSY attempts", res.Failed, maxSafeCPUSetWriteAttempts)
	}
}

func TestShrinkParentDefersWhenInitialBridgeHitsTextWrappedEBUSY(t *testing.T) {
	t.Parallel()

	parent := singleParentNode(t, machine.NewCPUSet(0))
	cg := newTopologyFakeCgroup()
	cg.cpus["parent"] = machine.NewCPUSet(0, 1, 2)
	cg.cpus["parent/child"] = machine.NewCPUSet(0, 1)
	cg.children["parent"] = []string{"child"}
	cg.applyErr["parent"] = errors.New(`write /sys/fs/cgroup/cpuset/parent/cpuset.cpus: device or resource busy`)
	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &DAGApplyResult{})

	err := writer.shrinkParentWithLiveChildUnion(parent, machine.NewCPUSet(0))
	if !IsDeferConvergenceError(err) {
		t.Fatalf("shrinkParentWithLiveChildUnion error = %v, want deferred convergence for safe bridge EBUSY", err)
	}
}

// TestFinalParentShrinkDoesNotMaskParentBelowChild verifies that a persistent
// EBUSY is NOT deferred when the parent is not a valid superset of its live
// children: the illegal state must surface as a raw error, never be hidden.
func TestFinalParentShrinkDoesNotMaskParentBelowChild(t *testing.T) {
	t.Parallel()

	node := singleParentNode(t, machine.NewCPUSet(0, 1))
	cg := newTopologyFakeCgroup()
	// parent {0} does NOT cover child {0,1}; superset precondition fails.
	cg.cpus["parent"] = machine.NewCPUSet(0)
	cg.cpus["parent/child"] = machine.NewCPUSet(0, 1)
	cg.children["parent"] = []string{"child"}
	cg.applyErr["parent"] = syscall.EBUSY
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &res)

	// target {0,1} keeps the reconcile-before-retry a no-op (child already within
	// target), so the only failing write is the parent's, with superset not held.
	err := writer.finalParentShrink(node, machine.NewCPUSet(0, 1))
	if IsDeferConvergenceError(err) {
		t.Fatalf("finalParentShrink deferred an illegal parent-below-child state, err=%v", err)
	}
	if !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("finalParentShrink error = %v, want raw EBUSY", err)
	}
}

// TestFinalParentShrinkDoesNotDeferNonEBUSY verifies that a non-EBUSY error is
// returned unchanged even when the parent superset precondition holds, so real
// failures (e.g. EINVAL) are never silently deferred.
func TestFinalParentShrinkDoesNotDeferNonEBUSY(t *testing.T) {
	t.Parallel()

	node := singleParentNode(t, machine.NewCPUSet(0, 1))
	cg := newTopologyFakeCgroup()
	cg.cpus["parent"] = machine.NewCPUSet(0, 1)
	cg.cpus["parent/child"] = machine.NewCPUSet(0)
	cg.children["parent"] = []string{"child"}
	cg.applyErr["parent"] = syscall.EINVAL
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &res)

	err := writer.finalParentShrink(node, machine.NewCPUSet(0, 1))
	if IsDeferConvergenceError(err) {
		t.Fatalf("finalParentShrink deferred a non-EBUSY error, err=%v", err)
	}
	if !errors.Is(err, syscall.EINVAL) {
		t.Fatalf("finalParentShrink error = %v, want raw EINVAL", err)
	}
}

// TestFinalParentShrinkConvergesWhenWritable verifies the next-reconcile
// success path: once the write no longer hits EBUSY the parent narrows to its
// target and no deferral is reported.
func TestFinalParentShrinkConvergesWhenWritable(t *testing.T) {
	t.Parallel()

	node := singleParentNode(t, machine.NewCPUSet(0, 1))
	cg := newTopologyFakeCgroup()
	cg.cpus["parent"] = machine.NewCPUSet(0, 1, 2)
	cg.cpus["parent/child"] = machine.NewCPUSet(0)
	cg.children["parent"] = []string{"child"}
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &res)

	if err := writer.finalParentShrink(node, machine.NewCPUSet(0, 1)); err != nil {
		t.Fatalf("finalParentShrink error = %v, want nil convergence", err)
	}
	if got := cg.cpus["parent"]; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("parent cpuset = %s, want 0-1 after successful shrink", got)
	}
}

// TestConvergeControlledNodesSwallowsDeferred verifies the admission-safety
// chain: a deferred generational parent shrink is counted in res.Deferred and
// NOT propagated as an error, so ApplyDAGDiff returns nil and the plugin does
// not fail Pod admission. The final narrow is failed only for the steady-state
// target, while the intermediate bridge is allowed to land.
func TestConvergeControlledNodesSwallowsDeferred(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "parent", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	// parent currently holds an extra cpu (2) and an uncontrolled child holds a
	// previous-generation cpu (2) that the narrowing must drain first.
	cg.cpus["parent"] = machine.NewCPUSet(0, 1, 2)
	cg.cpus["parent/child"] = machine.NewCPUSet(0, 2)
	cg.children["parent"] = []string{"child"}
	// Inject a persistent EBUSY only on the final steady-state narrow (0-1),
	// emulating a child bucket that still holds the old generation.
	cg.onApply = func(rel string, data *cgcommon.CPUSetData) {
		if rel == "parent" && data.CPUs == "0-1" {
			cg.applyErr["parent"] = syscall.EBUSY
		}
	}

	targets := map[string]machine.CPUSet{"parent": machine.NewCPUSet(0, 1)}
	res := DAGApplyResult{}
	in := DAGApplyInputs{DAG: dag, Cgroup: cg, CPUDetails: testCPUDetails()}
	if err := convergeControlledNodes(context.Background(), in, targets, false, &res); err != nil {
		t.Fatalf("convergeControlledNodes returned error = %v, want nil (deferred swallowed)", err)
	}
	if res.Deferred != 1 {
		t.Fatalf("res.Deferred = %d, want 1", res.Deferred)
	}
}

func TestConvergeControlledNodesSkipsExpandAfterDeferredShrink(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 2), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.cpus["primary/child"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"child"}
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		if rel == "primary/child" && data.CPUs == "0" {
			cg.cpus["primary/child"] = machine.NewCPUSet(0, 1)
		}
	}
	res := DAGApplyResult{}
	in := DAGApplyInputs{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	}

	if err := convergeControlledNodesWithBridgeConstraint(context.Background(), in, map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(0, 2),
	}, false, &res, true); err != nil {
		t.Fatalf("convergeControlledNodes returned error = %v, want nil (deferred swallowed)", err)
	}
	if res.Deferred == 0 {
		t.Fatalf("Deferred=0, want deferred shrink recorded")
	}
	if got := cg.cpus["primary"]; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("primary cpuset=%s, want unchanged bridge 0-1; writes=%#v", got.String(), cg.writes)
	}
}

func TestConstrainedWriteNodePreShrinksLiveChildrenBeforeParentWrite(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 2)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	node := dag.index["primary"]
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.cpus["primary/child"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"child"}
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriterForDAG(context.Background(), cg, dag, map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(0, 2),
	}, "0", &res).withConstrainBridgeGrowth(true)

	if err := writer.writeNode(node, machine.NewCPUSet(0, 2)); err != nil {
		t.Fatalf("writeNode returned error = %v; writes=%#v", err, cg.writes)
	}
	if len(cg.writes) < 2 {
		t.Fatalf("writes=%#v, want child shrink before parent write", cg.writes)
	}
	if got := cg.writes[0]; got.rel != "primary/child" || got.cpus != "0" {
		t.Fatalf("first write=%#v, want primary/child pre-shrink to 0; all writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["primary"]; !got.Equals(machine.NewCPUSet(0, 2)) {
		t.Fatalf("primary cpuset=%s, want 0,2", got.String())
	}
}

func TestConstrainedWriteNodePreShrinksLiveChildrenEvenWhenParentAlreadyCovered(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 2)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	node := dag.index["primary"]
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.cpus["primary/child"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"child"}
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriterForDAG(context.Background(), cg, dag, map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(0, 2),
	}, "0", &res).withConstrainBridgeGrowth(true)

	if err := writer.writeNode(node, machine.NewCPUSet(0, 2)); err != nil {
		t.Fatalf("writeNode returned error = %v; writes=%#v", err, cg.writes)
	}
	if len(cg.writes) < 2 {
		t.Fatalf("writes=%#v, want child shrink before parent write", cg.writes)
	}
	if got := cg.writes[0]; got.rel != "primary/child" || got.cpus != "0" {
		t.Fatalf("first write=%#v, want primary/child pre-shrink to 0; all writes=%#v", got, cg.writes)
	}
}

func TestConstrainedWriteNodeDefersWhenLiveChildSnapsBackBeforeParentWrite(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 2)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	node := dag.index["primary"]
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.cpus["primary/child"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"child"}
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		if rel == "primary/child" && data.CPUs == "0" {
			cg.cpus["primary/child"] = machine.NewCPUSet(0, 1)
		}
		if rel == "primary" {
			t.Fatalf("parent write should be deferred while child still owns CPU 1; data=%+v writes=%#v", data, cg.writes)
		}
	}
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriterForDAG(context.Background(), cg, dag, map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(0, 2),
	}, "0", &res).withConstrainBridgeGrowth(true)

	err = writer.writeNode(node, machine.NewCPUSet(0, 2))
	if !IsDeferConvergenceError(err) {
		t.Fatalf("writeNode error=%v, want deferred convergence; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["primary"]; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("primary cpuset=%s, want unchanged 0-1", got.String())
	}
}

// reclaimBucketTOCTOUNodes builds the two-node DAG (reclaim root + one NUMA
// bucket) used by the TOCTOU straggler tests and returns the bucket node.
func reclaimBucketTOCTOUNodes(t *testing.T, rootCPUs, bucketCPUs machine.CPUSet) *TopoNode {
	t.Helper()
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: rootCPUs, Mems: "0-1"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: bucketCPUs, Mems: "1", Metadata: map[string]string{"numa": "1"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	return dag.index["kubesandbox/reclaimed-1"]
}

// TestShrinkReclaimBucketDefersOnStragglerChildAfterRetries reproduces the
// high-churn TOCTOU that broke Pod admission on the node: a live (uncontrolled)
// reclaim child re-inherits the previous generation (28-30,76-78) in the window
// between the descendant normalization scan and the live-child recheck, so it is
// still outside the steady-state target (33-34,81-82) on every one of the
// bounded shrink attempts. The bucket itself stays a valid cgroup v1 superset of
// that child, so the path must NOT fail with a hard children_not_ready; it must
// return errDeferConvergence and let the next reconcile finish the narrowing.
func TestShrinkReclaimBucketDefersOnStragglerChildAfterRetries(t *testing.T) {
	t.Parallel()

	oldGen := machine.NewCPUSet(28, 29, 30, 76, 77, 78)
	target := machine.NewCPUSet(33, 34, 81, 82)
	// The bucket already bridges old+new generations, so it covers every live
	// child (target is a subset of it): the superset precondition holds.
	bucketSuperset := oldGen.Union(target)
	bucket := reclaimBucketTOCTOUNodes(t, bucketSuperset, bucketSuperset)

	childRel := "kubesandbox/reclaimed-1/sandbox0"
	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox"] = bucketSuperset
	cg.cpus["kubesandbox/reclaimed-1"] = bucketSuperset
	cg.cpus[childRel] = oldGen
	cg.children["kubesandbox"] = []string{"reclaimed-1"}
	cg.children["kubesandbox/reclaimed-1"] = []string{"sandbox0"}
	// TOCTOU: whatever the normalization writes, the child immediately snaps
	// back to the previous generation before the recheck reads it. This models a
	// straggler owned by a separate advisor-controlled transition that keeps
	// re-inheriting the bridged parent's old segment across every attempt.
	var normalizeWrites int
	cg.afterApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == childRel {
			normalizeWrites++
			cg.cpus[childRel] = oldGen.Clone()
		}
	}

	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &res)
	err := writer.shrinkParentWithLiveChildUnion(bucket, target)
	if !IsDeferConvergenceError(err) {
		t.Fatalf("shrinkParentWithLiveChildUnion error = %v, want deferred-convergence sentinel (writes=%#v)", err, cg.writes)
	}
	// The shrink must have re-run normalization the full budget before deferring,
	// not tripped children_not_ready on first sight.
	if normalizeWrites != maxReclaimBucketShrinkAttempts {
		t.Fatalf("normalize attempts = %d, want %d bounded retries", normalizeWrites, maxReclaimBucketShrinkAttempts)
	}
	// The bucket stayed a valid superset; it must not have been clamped below its
	// live child in the process.
	if got := cg.cpus["kubesandbox/reclaimed-1"]; !oldGen.IsSubsetOf(got) {
		t.Fatalf("bucket cpuset = %s no longer covers live child %s", got, oldGen)
	}
}

func TestShrinkParentKeepsBridgeAndDoesNotDeferWhenControlledChildAlreadyOutsideTarget(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	parent := dag.index["kubesandbox"]
	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox"] = machine.NewCPUSet(1, 2)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(2)
	cg.children["kubesandbox"] = []string{"reclaimed-1"}
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriterForDAG(context.Background(), cg, dag, map[string]machine.CPUSet{
		"kubesandbox":             machine.NewCPUSet(1),
		"kubesandbox/reclaimed-1": machine.NewCPUSet(2),
	}, "0", &res)

	if err := writer.shrinkParentWithLiveChildUnion(parent, machine.NewCPUSet(1)); err != nil {
		t.Fatalf("shrinkParentWithLiveChildUnion error=%v, want nil; writes=%#v", err, cg.writes)
	}
	for _, write := range cg.writes {
		if write.rel == "kubesandbox" && write.cpus == "1" {
			t.Fatalf("parent was shrunk below controlled child instead of keeping bridge; writes=%#v", cg.writes)
		}
	}
	if got := cg.cpus["kubesandbox"]; !got.Equals(machine.NewCPUSet(1, 2)) {
		t.Fatalf("parent cpuset=%s, want bridge 1-2", got.String())
	}
}

func TestWriteNodeKeepsBridgeAndDoesNotWriteParentBelowControlledChild(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	parent := dag.index["kubesandbox"]
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox"] = machine.NewCPUSet(1, 2)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(2)
	cg.children["kubesandbox"] = []string{"reclaimed-1"}
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriterForDAG(context.Background(), cg, dag, map[string]machine.CPUSet{
		"kubesandbox":             machine.NewCPUSet(1),
		"kubesandbox/reclaimed-1": machine.NewCPUSet(2),
	}, "0", &res)

	if err := writer.writeNode(parent, machine.NewCPUSet(1)); err != nil {
		t.Fatalf("writeNode error=%v, want nil bridge/no-op; writes=%#v", err, cg.writes)
	}
	for _, write := range cg.writes {
		if write.rel == "kubesandbox" && write.cpus == "1" {
			t.Fatalf("writeNode wrote parent below controlled child; writes=%#v", cg.writes)
		}
	}
	if got := cg.cpus["kubesandbox"]; !got.Equals(machine.NewCPUSet(1, 2)) {
		t.Fatalf("parent cpuset=%s, want bridge 1-2", got.String())
	}
}

func TestWriteDynamicRelWidensIntermediateParentBeforeLeafGrow(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1, 2), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox"] = machine.NewCPUSet(1, 2)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(1, 2)
	cg.cpus["kubesandbox/reclaimed-1/pod"] = machine.NewCPUSet(1)
	cg.cpus["kubesandbox/reclaimed-1/pod/container"] = machine.NewCPUSet(1)
	cg.children["kubesandbox"] = []string{"reclaimed-1"}
	cg.children["kubesandbox/reclaimed-1"] = []string{"pod"}
	cg.children["kubesandbox/reclaimed-1/pod"] = []string{"container"}
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriterForDAG(context.Background(), cg, dag, map[string]machine.CPUSet{
		"kubesandbox":             machine.NewCPUSet(1, 2),
		"kubesandbox/reclaimed-1": machine.NewCPUSet(2),
	}, "0", &res)

	err = writer.writeDynamicRel("kubesandbox/reclaimed-1/pod/container", machine.NewCPUSet(2), "1")
	if err != nil {
		t.Fatalf("writeDynamicRel error=%v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubesandbox/reclaimed-1/pod"]; !machine.NewCPUSet(2).IsSubsetOf(got) {
		t.Fatalf("intermediate parent cpuset=%s, want it to cover leaf target 2; writes=%#v", got.String(), cg.writes)
	}
	if got := cg.cpus["kubesandbox/reclaimed-1/pod/container"]; !got.Equals(machine.NewCPUSet(2)) {
		t.Fatalf("leaf cpuset=%s, want 2; writes=%#v", got.String(), cg.writes)
	}
}

func TestWriteNodeKeepsBridgeAndDoesNotShrinkBucketBelowLiveChild(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1, 2), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(1), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	bucket := dag.index["kubesandbox/reclaimed-1"]
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox"] = machine.NewCPUSet(1, 2)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(1, 2)
	cg.cpus["kubesandbox/reclaimed-1/pod"] = machine.NewCPUSet(2)
	cg.children["kubesandbox"] = []string{"reclaimed-1"}
	cg.children["kubesandbox/reclaimed-1"] = []string{"pod"}
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriterForDAG(context.Background(), cg, dag, map[string]machine.CPUSet{
		"kubesandbox":             machine.NewCPUSet(1, 2),
		"kubesandbox/reclaimed-1": machine.NewCPUSet(1),
	}, "0", &res)

	if err := writer.writeNode(bucket, machine.NewCPUSet(1)); err != nil {
		t.Fatalf("writeNode error=%v, want nil bridge/no-op; writes=%#v", err, cg.writes)
	}
	for _, write := range cg.writes {
		if write.rel == "kubesandbox/reclaimed-1" && write.cpus == "1" {
			t.Fatalf("bucket was shrunk below live child instead of keeping bridge; writes=%#v", cg.writes)
		}
	}
	if got := cg.cpus["kubesandbox/reclaimed-1"]; !got.Equals(machine.NewCPUSet(1, 2)) {
		t.Fatalf("bucket cpuset=%s, want bridge 1-2", got.String())
	}
}

// TestShrinkReclaimBucketSurfacesHardErrorWhenParentNotSuperset verifies the
// negative half of the guard: when a straggler child holds a generation the
// bucket does NOT cover (a genuine illegal parent-below-child state), the shrink
// must NOT hide it behind a deferral. The hard children_not_ready error is
// surfaced so a real ownership violation stays visible.
func TestShrinkReclaimBucketSurfacesHardErrorWhenParentNotSuperset(t *testing.T) {
	t.Parallel()

	oldGen := machine.NewCPUSet(28, 29, 30, 76, 77, 78)
	target := machine.NewCPUSet(33, 34, 81, 82)
	// The bucket already sits at the steady-state target and does NOT cover the
	// straggler's old generation: the superset precondition fails.
	bucket := reclaimBucketTOCTOUNodes(t, target, target)

	childRel := "kubesandbox/reclaimed-1/sandbox0"
	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox"] = target
	cg.cpus["kubesandbox/reclaimed-1"] = target
	cg.cpus[childRel] = oldGen
	cg.children["kubesandbox"] = []string{"reclaimed-1"}
	cg.children["kubesandbox/reclaimed-1"] = []string{"sandbox0"}
	cg.afterApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == childRel {
			cg.cpus[childRel] = oldGen.Clone()
		}
	}

	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &res)
	err := writer.shrinkParentWithLiveChildUnion(bucket, target)
	if err == nil {
		t.Fatalf("shrinkParentWithLiveChildUnion error = nil, want hard children_not_ready")
	}
	if IsDeferConvergenceError(err) {
		t.Fatalf("shrinkParentWithLiveChildUnion deferred an illegal parent-below-child state, err=%v", err)
	}
	if !strings.Contains(err.Error(), "children_not_ready") {
		t.Fatalf("shrinkParentWithLiveChildUnion error = %v, want children_not_ready", err)
	}
}

// TestShrinkReclaimBucketIgnoresDescendantThatDisappearsDuringNormalization
// covers the live-node race observed on the node: ListChildren sees an
// uncontrolled sandbox, but kubelet removes that sandbox before recursion can
// enumerate its children. A vanished cgroup has no descendant left to
// normalize, so it must not fail the whole bucket shrink or Pod admission.
func TestShrinkReclaimBucketIgnoresDescendantThatDisappearsDuringNormalization(t *testing.T) {
	t.Parallel()

	target := machine.NewCPUSet(33, 34, 81, 82)
	bucket := reclaimBucketTOCTOUNodes(t, target, target)
	childRel := "kubesandbox/reclaimed-1/sandbox0"

	base := newTopologyFakeCgroup()
	base.cpus["kubesandbox"] = target
	base.cpus["kubesandbox/reclaimed-1"] = target
	base.cpus[childRel] = target
	base.children["kubesandbox"] = []string{"reclaimed-1"}
	base.children["kubesandbox/reclaimed-1"] = []string{"sandbox0"}
	cg := &disappearingReclaimDescendantCgroup{
		topologyFakeCgroup: base,
		disappearingRel:    childRel,
	}

	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &DAGApplyResult{})
	if err := writer.shrinkParentWithLiveChildUnion(bucket, target); err != nil {
		t.Fatalf("shrinkParentWithLiveChildUnion error = %v, want nil after descendant removal", err)
	}
	if !cg.disappeared {
		t.Fatal("disappearing descendant was not traversed")
	}
}
