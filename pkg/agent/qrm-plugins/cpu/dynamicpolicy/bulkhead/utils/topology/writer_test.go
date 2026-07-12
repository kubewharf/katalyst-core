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
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"testing"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type cpusetWrite struct {
	rel  string
	cpus string
	mems string
}

type topologyFakeCgroup struct {
	cgroupclient.FakeCgroupClient

	version  cgroupclient.CgroupVersion
	cpus     map[string]machine.CPUSet
	children map[string][]string
	writes   []cpusetWrite
	failRel  map[string]bool
}

func newTopologyFakeCgroup() *topologyFakeCgroup {
	return &topologyFakeCgroup{
		version:  cgroupclient.CgroupVersionV1,
		cpus:     map[string]machine.CPUSet{},
		children: map[string][]string{},
		failRel:  map[string]bool{},
	}
}

func (f *topologyFakeCgroup) Version(context.Context) cgroupclient.CgroupVersion {
	return f.version
}

func (f *topologyFakeCgroup) ReadCPUSet(_ context.Context, rel string) (machine.CPUSet, error) {
	if cpus, ok := f.cpus[rel]; ok {
		return cpus.Clone(), nil
	}
	return machine.NewCPUSet(), nil
}

func (f *topologyFakeCgroup) ApplyCPUSet(_ context.Context, rel string, data *cgcommon.CPUSetData) error {
	target := machine.MustParse(data.CPUs)
	if f.failRel[rel] {
		return fmt.Errorf("forced failure @ %s", rel)
	}
	if f.version != cgroupclient.CgroupVersionV2 || !target.IsEmpty() {
		for _, child := range f.children[rel] {
			childRel := filepath.Join(rel, child)
			if childCPUs := f.cpus[childRel]; !childCPUs.IsEmpty() && !childCPUs.IsSubsetOf(target) {
				return fmt.Errorf("child %s cpuset %s is outside parent target %s", childRel, childCPUs.String(), target.String())
			}
		}
	}
	f.cpus[rel] = target.Clone()
	f.writes = append(f.writes, cpusetWrite{rel: rel, cpus: data.CPUs, mems: data.Mems})
	return nil
}

func (f *topologyFakeCgroup) ListChildren(_ context.Context, rel string) ([]string, error) {
	children := append([]string(nil), f.children[rel]...)
	sort.Strings(children)
	return children, nil
}

func TestApplyDAGDiffRejectsDisjointReplacement(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
		{Rel: "primary/pod", ParentRel: "primary", Role: TopoNodeRoleReclaimSibling, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.cpus["primary/pod"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"pod"}

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"primary/pod": machine.NewCPUSet(2, 3),
		},
	})
	if err == nil {
		t.Fatalf("expected disjoint replacement validation error, got result=%+v writes=%#v", res, cg.writes)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("disjoint validation should fail before writes, got %#v", cg.writes)
	}
}

func TestApplyDAGDiffShrinksIntersectionBeforeExpandingOverlapReplacement(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2, 3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1, 2)

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	want := []cpusetWrite{
		{rel: "primary", cpus: "1-2", mems: "0"},
		{rel: "primary", cpus: "1-3", mems: "0"},
	}
	if !reflect.DeepEqual(cg.writes, want) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, want)
	}
}

func TestApplyDAGDiffShrinksBeforeExpands(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "domain-a", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0)},
		{Rel: "domain-b", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(2, 3)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["domain-a"] = machine.NewCPUSet(0, 1)
	cg.cpus["domain-b"] = machine.NewCPUSet(2)

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	want := []cpusetWrite{
		{rel: "domain-a", cpus: "0"},
		{rel: "domain-b", cpus: "2-3"},
	}
	if !reflect.DeepEqual(cg.writes[:2], want) {
		t.Fatalf("writes = %#v, want prefix %#v", cg.writes, want)
	}
}

func TestApplyDAGDiffValidationAndFailurePaths(t *testing.T) {
	t.Parallel()

	if _, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{}); err == nil {
		t.Fatalf("expected nil DAG error")
	}
	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	if _, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{DAG: dag}); err == nil {
		t.Fatalf("expected nil cgroup error")
	}

	cg := newTopologyFakeCgroup()
	cg.failRel["primary"] = true
	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:              dag,
		Cgroup:           cg,
		SkipObservedRead: true,
	})
	if err == nil {
		t.Fatalf("expected apply error")
	}
	if res.Failed == 0 {
		t.Fatalf("expected failed count, got %+v", res)
	}
}

func TestApplyDAGDiffExpandsUnmanagedDescendants(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.children["primary"] = []string{"burstable"}
	cg.children["primary/burstable"] = []string{"pod"}

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if res.Applied == 0 {
		t.Fatalf("expected descendant writes, got %+v", res)
	}
	if got := cg.cpus["primary/burstable/pod"].String(); got != "0-1" {
		t.Fatalf("leaf cpuset = %s, want 0-1; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffExpandsEmptyTargetsToUnmanagedDescendantsV2(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet()}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.version = cgroupclient.CgroupVersionV2
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.cpus["primary/burstable"] = machine.NewCPUSet(0, 1)
	cg.cpus["primary/burstable/pod-a"] = machine.NewCPUSet(0, 1)
	cg.cpus["primary/burstable/pod-a/container-a"] = machine.NewCPUSet(0)
	cg.children["primary"] = []string{"burstable"}
	cg.children["primary/burstable"] = []string{"pod-a"}
	cg.children["primary/burstable/pod-a"] = []string{"container-a"}

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:              dag,
		Cgroup:           cg,
		SkipObservedRead: true,
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"primary/burstable/pod-a/container-a": machine.NewCPUSet(0),
		},
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if res.Applied == 0 {
		t.Fatalf("expected empty target writes, got %+v", res)
	}

	wantCPUSetByRel := map[string]string{
		"primary":                             "",
		"primary/burstable":                   "",
		"primary/burstable/pod-a":             "",
		"primary/burstable/pod-a/container-a": "0",
	}
	for rel, want := range wantCPUSetByRel {
		if got := cg.cpus[rel].String(); got != want {
			t.Fatalf("cpuset @ %s = %q, want %q; writes=%#v", rel, got, want, cg.writes)
		}
	}
}

func TestApplyDAGDiffSkipsEmptyTargetsV1(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet()}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("empty v1 target should not be written, got %#v", cg.writes)
	}
	if res.Skipped == 0 {
		t.Fatalf("expected skipped count, got %+v", res)
	}
}
