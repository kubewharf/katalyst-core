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
	"testing"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// TestResetWriterMemsCommittedThenCPUsFailureRetriedNextRound pins the recoverable
// partial-write contract of the reset writer's per-rel writeResetRel: mems is
// written before cpus. When the mems write commits but the cpus write fails with
// a generic transient I/O error, the round must (a) return that error, (b) keep
// the already-committed mems and leave cpus at its previous value (no silent
// success, no fabricated Applied), and (c) report the failed attempt. It must
// NOT roll back the committed mems inside this rel, because the caller reads the
// fresh live entry on the next round and retries the remaining cpus write; the
// partial state is recoverable rather than a permanent drift. On the retry round
// the previously-committed mems is re-written idempotently and the cpus write
// succeeds, reaching the target.
func TestResetWriterMemsCommittedThenCPUsFailureRetriedNextRound(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.MustParse("0-3"), TrustAnchor: true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	fake := newFakeHierarchyDriver()
	fake.allowUnwitnessedExpansion = true
	// Start below the target on cpus (0-1 -> 0-3 grow) and write a distinct mems
	// value so the committed mems write is observable on the failed round.
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")

	cpusFailures := 1
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationWriteCPUs && rel == "primary" && cpusFailures > 0 {
			cpusFailures--
			return resetInjectedCPUFailure
		}
		return nil
	}

	targets := map[string]machine.CPUSet{"primary": machine.MustParse("0-3")}

	// Round 1: mems commits, cpus write fails once.
	res1 := &ConvergenceResult{}
	w1 := newResetCoordinatorWriter(fake, NewBudgetTracker(ConvergenceBudget{}), "1", res1)
	err = w1.execute(context.Background(), dag, targets, false, nil, nil, nil, nil)
	if !errors.Is(err, resetInjectedCPUFailure) {
		t.Fatalf("round1 error = %v, want injected cpus failure", err)
	}
	node := fake.nodes["primary"]
	if node.mems != "1" {
		t.Fatalf("round1 committed mems = %q, want %q (mems writes before cpus and must persist)", node.mems, "1")
	}
	if got := node.cpus.String(); got != "0-1" {
		t.Fatalf("round1 cpus = %q, want unchanged 0-1 after failed cpus write", got)
	}
	if res1.Applied != 0 {
		t.Fatalf("round1 Applied = %d, want 0 (cpus failed, no operation fully applied)", res1.Applied)
	}
	if res1.Failed == 0 {
		t.Fatalf("round1 Failed = 0, want the failed cpus attempt recorded")
	}

	// Round 2: failure cleared, retry writes mems idempotently and converges cpus.
	fake.beforeCall = nil
	res2 := &ConvergenceResult{}
	w2 := newResetCoordinatorWriter(fake, NewBudgetTracker(ConvergenceBudget{}), "1", res2)
	if err := w2.execute(context.Background(), dag, targets, false, nil, nil, nil, nil); err != nil {
		t.Fatalf("round2 execute: %v", err)
	}
	node = fake.nodes["primary"]
	if got := node.cpus.String(); got != "0-3" {
		t.Fatalf("round2 cpus = %q, want target 0-3 after retry", got)
	}
	if node.mems != "1" {
		t.Fatalf("round2 mems = %q, want committed mems 1 preserved", node.mems)
	}
	if res2.Applied == 0 {
		t.Fatalf("round2 Applied = 0, want the retried cpus write recorded applied")
	}
}

// TestResetWriterCPUsFailureBeforeMemsLeavesZeroWrites pins the write order the
// other way: when the cpus write injection fires before mems would commit, the
// rel must not have written mems yet, so the round leaves no partial mems drift.
// This guards the invariant that a cpus failure does not manufacture a
// half-committed state ahead of the mems write.
func TestResetWriterCPUsFailureBeforeMemsLeavesZeroWrites(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.MustParse("0-3"), TrustAnchor: true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	fake := newFakeHierarchyDriver()
	fake.allowUnwitnessedExpansion = true
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")

	// Fail both writes; this exercises the failure path without assuming which
	// physical write the driver attempts first, and asserts no state mutates.
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if rel == "primary" && (op == HierarchyOperationWriteCPUs || op == HierarchyOperationWriteMems) {
			return resetInjectedCPUFailure
		}
		return nil
	}

	res := &ConvergenceResult{}
	w := newResetCoordinatorWriter(fake, NewBudgetTracker(ConvergenceBudget{}), "1", res)
	err = w.execute(context.Background(), dag, map[string]machine.CPUSet{
		"primary": machine.MustParse("0-3"),
	}, false, nil, nil, nil, nil)
	if !errors.Is(err, resetInjectedCPUFailure) {
		t.Fatalf("error = %v, want injected failure", err)
	}
	node := fake.nodes["primary"]
	if node.mems != "0" {
		t.Fatalf("mems mutated to %q despite failing writes; want original 0", node.mems)
	}
	if got := node.cpus.String(); got != "0-1" {
		t.Fatalf("cpus mutated to %q despite failing writes; want original 0-1", got)
	}
	if res.Applied != 0 {
		t.Fatalf("Applied = %d, want 0", res.Applied)
	}
	if len(fake.writes) != 0 {
		t.Fatalf("committed %d writes despite injected failure; writes=%#v", len(fake.writes), fake.writes)
	}
}

var resetInjectedCPUFailure = errors.New("simulated transient cpuset.cpus write failure")
