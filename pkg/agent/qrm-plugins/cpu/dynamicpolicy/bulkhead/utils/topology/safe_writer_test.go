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
	"reflect"
	"syscall"
	"testing"

	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type lateChildDuringParentShrinkCgroup struct {
	*topologyFakeCgroup

	parentShrinkAttempts int
	injected             bool
	lateChildReads       int
	listsAfterInjection  int
}

type retryObservationCgroup struct {
	*topologyFakeCgroup

	childLists int
	childReads int
}

func (f *retryObservationCgroup) ReadCPUSet(ctx context.Context, rel string) (machine.CPUSet, error) {
	if rel == "parent/child" {
		f.childReads++
	}
	return f.topologyFakeCgroup.ReadCPUSet(ctx, rel)
}

func (f *retryObservationCgroup) ListChildren(ctx context.Context, rel string) ([]string, error) {
	if rel == "parent" {
		f.childLists++
	}
	return f.topologyFakeCgroup.ListChildren(ctx, rel)
}

func (f *lateChildDuringParentShrinkCgroup) ApplyCPUSet(ctx context.Context, rel string, data *cgcommon.CPUSetData) error {
	if rel == "parent" && data.CPUs == "0-1" {
		f.parentShrinkAttempts++
		if f.parentShrinkAttempts == 1 {
			f.cpus["parent/late-child"] = machine.NewCPUSet(1, 2)
			f.children["parent"] = []string{"late-child"}
			f.injected = true
			return syscall.EBUSY
		}
	}
	return f.topologyFakeCgroup.ApplyCPUSet(ctx, rel, data)
}

func (f *lateChildDuringParentShrinkCgroup) ReadCPUSet(ctx context.Context, rel string) (machine.CPUSet, error) {
	if f.injected && rel == "parent/late-child" {
		f.lateChildReads++
	}
	return f.topologyFakeCgroup.ReadCPUSet(ctx, rel)
}

func (f *lateChildDuringParentShrinkCgroup) ListChildren(ctx context.Context, rel string) ([]string, error) {
	if f.injected && rel == "parent" {
		f.listsAfterInjection++
	}
	return f.topologyFakeCgroup.ListChildren(ctx, rel)
}

func TestApplyDAGDiffRetriesParentShrinkAfterEBUSYWithLateChild(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "parent", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	base := newTopologyFakeCgroup()
	base.cpus["parent"] = machine.NewCPUSet(0, 1, 2, 3)
	cg := &lateChildDuringParentShrinkCgroup{topologyFakeCgroup: base}

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v result=%+v", err, cg.writes, res)
	}

	if got, want := cg.cpus["parent"], machine.NewCPUSet(0, 1); !got.Equals(want) {
		t.Fatalf("parent cpuset = %s, want %s; writes=%#v", got.String(), want.String(), cg.writes)
	}
	if got, want := cg.cpus["parent/late-child"], machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("late child cpuset = %s, want %s; writes=%#v", got.String(), want.String(), cg.writes)
	}
	if cg.parentShrinkAttempts < 2 {
		t.Fatalf("parent shrink attempts = %d, want at least 2; writes=%#v", cg.parentShrinkAttempts, cg.writes)
	}
	if cg.listsAfterInjection == 0 || cg.lateChildReads == 0 {
		t.Fatalf("late child was not re-listed and read: lists=%d reads=%d; writes=%#v",
			cg.listsAfterInjection, cg.lateChildReads, cg.writes)
	}
	wantWrites := []cpusetWrite{
		{rel: "parent", cpus: "0-2", mems: ""},
		{rel: "parent/late-child", cpus: "1", mems: ""},
		{rel: "parent", cpus: "0-1", mems: ""},
	}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes=%#v, want fresh-child bridge and final parent targets=%#v", cg.writes, wantWrites)
	}

	lateChildWrite, parentWrite := -1, -1
	for i, write := range cg.writes {
		if write.rel == "parent/late-child" && write.cpus == "1" {
			lateChildWrite = i
		}
		if write.rel == "parent" && write.cpus == "0-1" {
			parentWrite = i
		}
	}
	if lateChildWrite < 0 || parentWrite < 0 || lateChildWrite >= parentWrite {
		t.Fatalf("writes=%#v, want late child convergence before final parent shrink", cg.writes)
	}
}

func TestSafeCPUSetWriterLimitsEBUSYRetries(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "parent", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	base := newTopologyFakeCgroup()
	base.applyErr["parent"] = syscall.EBUSY
	base.cpus["parent/child"] = machine.NewCPUSet(0)
	base.children["parent"] = []string{"child"}
	cg := &retryObservationCgroup{topologyFakeCgroup: base}
	attempts := 0
	cg.onApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == "parent" {
			attempts++
		}
	}
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &res)

	err = writer.writeNode(dag.index["parent"], machine.NewCPUSet(0, 1))
	if !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("writeNode error = %v, want EBUSY", err)
	}
	if attempts != maxSafeCPUSetWriteAttempts {
		t.Fatalf("EBUSY attempts = %d, want %d", attempts, maxSafeCPUSetWriteAttempts)
	}
	if want := maxSafeCPUSetWriteAttempts - 1; cg.childLists != want || cg.childReads != want {
		t.Fatalf("fresh child observations after retryable EBUSY = lists:%d reads:%d, want both %d",
			cg.childLists, cg.childReads, want)
	}
	if res.Attempted != maxSafeCPUSetWriteAttempts || res.Failed != maxSafeCPUSetWriteAttempts {
		t.Fatalf("result = %+v, want all attempts failed", res)
	}
}

func TestSafeCPUSetWriterReturnsNonEBUSYImmediately(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "parent", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	wantErr := errors.New("device or resource busy")
	cg := newTopologyFakeCgroup()
	cg.applyErr["parent"] = wantErr
	attempts := 0
	cg.onApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == "parent" {
			attempts++
		}
	}
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &res)

	err = writer.writeNode(dag.index["parent"], machine.NewCPUSet(0, 1))
	if !errors.Is(err, wantErr) {
		t.Fatalf("writeNode error = %v, want %v", err, wantErr)
	}
	if attempts != 1 {
		t.Fatalf("non-EBUSY attempts = %d, want 1", attempts)
	}
	if res.Attempted != 1 || res.Failed != 1 {
		t.Fatalf("result = %+v, want attempted=1 failed=1", res)
	}
}

func TestSafeCPUSetWriterGrowsParentBeforeChild(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "parent", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "parent/child", ParentRel: "parent", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	parent := dag.index["parent"]
	child := dag.index["parent/child"]
	cg := newTopologyFakeCgroup()
	cg.cpus[parent.Rel] = machine.NewCPUSet(0)
	cg.cpus[child.Rel] = machine.NewCPUSet()
	res := DAGApplyResult{}

	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &res)
	if err := writer.growNodeWithParentBridge(child, machine.NewCPUSet(1)); err != nil {
		t.Fatalf("growNodeWithParentBridge: %v", err)
	}
	wantWrites := []cpusetWrite{
		{rel: "parent", cpus: "0-1", mems: "0"},
		{rel: "parent/child", cpus: "1", mems: "0"},
	}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, wantWrites)
	}
	if res.Attempted != 2 || res.Applied != 2 || res.Failed != 0 {
		t.Fatalf("result = %+v, want attempted=2 applied=2 failed=0", res)
	}
}

func TestSafeCPUSetWriterGrowsAncestorBeforeParentBridge(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "root", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "root/parent", ParentRel: "root", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "root/parent/child", ParentRel: "root/parent", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	root := dag.index["root"]
	parent := dag.index["root/parent"]
	child := dag.index["root/parent/child"]
	cg := newTopologyFakeCgroup()
	cg.cpus[root.Rel] = machine.NewCPUSet(0)
	cg.cpus[parent.Rel] = machine.NewCPUSet(0)
	cg.cpus[child.Rel] = machine.NewCPUSet()
	res := DAGApplyResult{}

	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &res)
	if err := writer.growNodeWithParentBridge(child, machine.NewCPUSet(1)); err != nil {
		t.Fatalf("growNodeWithParentBridge: %v", err)
	}
	wantWrites := []cpusetWrite{
		{rel: "root", cpus: "0-1", mems: "0"},
		{rel: "root/parent", cpus: "0-1", mems: "0"},
		{rel: "root/parent/child", cpus: "1", mems: "0"},
	}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, wantWrites)
	}
}

func TestSafeCPUSetWriterShrinksParentAfterLiveChildren(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "parent", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "parent/child", ParentRel: "parent", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	parent := dag.index["parent"]
	child := dag.index["parent/child"]
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus[parent.Rel] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus[child.Rel] = machine.NewCPUSet(1, 2)
	cg.children[parent.Rel] = []string{"child"}
	res := DAGApplyResult{}

	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &res)
	if err := writer.shrinkParentWithLiveChildUnion(parent, machine.NewCPUSet(0, 1)); err != nil {
		t.Fatalf("shrinkParentWithLiveChildUnion: %v", err)
	}
	wantWrites := []cpusetWrite{
		{rel: "parent", cpus: "0-2", mems: "0"},
		{rel: "parent/child", cpus: "1", mems: ""},
		{rel: "parent", cpus: "0-1", mems: "0"},
	}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, wantWrites)
	}
	if got, want := cg.cpus[parent.Rel], machine.NewCPUSet(0, 1); !got.Equals(want) {
		t.Fatalf("parent cpuset = %s, want %s", got.String(), want.String())
	}
	if got, want := cg.cpus[child.Rel], machine.NewCPUSet(1); !got.Equals(want) {
		t.Fatalf("child cpuset = %s, want %s", got.String(), want.String())
	}
}
