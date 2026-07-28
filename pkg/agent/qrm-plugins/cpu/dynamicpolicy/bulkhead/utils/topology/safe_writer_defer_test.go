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
	"syscall"
	"testing"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

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
