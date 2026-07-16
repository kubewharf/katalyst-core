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
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"syscall"
	"testing"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type cpusetWrite struct {
	rel            string
	cpus           string
	mems           string
	writeEmptyCPUs bool
	writeEmptyMems bool
}

type topologyFakeCgroup struct {
	cgroupclient.FakeCgroupClient

	version    cgroupclient.CgroupVersion
	cpus       map[string]machine.CPUSet
	children   map[string][]string
	files      map[string]map[string][]byte
	writes     []cpusetWrite
	failRel    map[string]bool
	applyErr   map[string]error
	onApply    func(rel string, data *cgcommon.CPUSetData)
	afterApply func(rel string, data *cgcommon.CPUSetData)

	enforceParentContainsTarget bool
}

func newTopologyFakeCgroup() *topologyFakeCgroup {
	return &topologyFakeCgroup{
		version:  cgroupclient.CgroupVersionV1,
		cpus:     map[string]machine.CPUSet{},
		children: map[string][]string{},
		files:    map[string]map[string][]byte{},
		failRel:  map[string]bool{},
		applyErr: map[string]error{},
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
	if f.onApply != nil {
		f.onApply(rel, data)
	}
	if err := f.applyErr[rel]; err != nil {
		return err
	}
	if f.failRel[rel] {
		return fmt.Errorf("forced failure @ %s", rel)
	}
	if f.enforceParentContainsTarget {
		parent := filepath.Dir(rel)
		if parent != "." && parent != rel {
			if parentCPUs, ok := f.cpus[parent]; ok && !target.IsSubsetOf(parentCPUs) {
				return fmt.Errorf("target %s is outside parent %s cpuset %s", target.String(), parent, parentCPUs.String())
			}
		}
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
	f.writes = append(f.writes, cpusetWrite{
		rel:            rel,
		cpus:           data.CPUs,
		mems:           data.Mems,
		writeEmptyCPUs: data.WriteEmptyCPUs,
		writeEmptyMems: data.WriteEmptyMems,
	})
	if f.afterApply != nil {
		f.afterApply(rel, data)
	}
	return nil
}

func (f *topologyFakeCgroup) ListChildren(_ context.Context, rel string) ([]string, error) {
	children := append([]string(nil), f.children[rel]...)
	sort.Strings(children)
	return children, nil
}

func (f *topologyFakeCgroup) ReadCgroupFile(_ context.Context, rel, file string) ([]byte, error) {
	if byFile, ok := f.files[rel]; ok {
		if raw, ok := byFile[file]; ok {
			return append([]byte(nil), raw...), nil
		}
	}
	return nil, nil
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
		DAG:                  dag,
		Cgroup:               cg,
		Mems:                 "0",
		KubeManagedRelPrefix: "kubepods",
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

func TestApplyDAGDiffPreShrinksSiblingMovesBeforeTargetGrow(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 2, 6)},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4, 5, 99)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2, 99)
	cg.cpus["kubesandbox"] = machine.NewCPUSet(3, 4, 5, 6)
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		overlap := cg.cpus["kubepods"].Intersection(cg.cpus["kubesandbox"])
		if !overlap.IsEmpty() {
			t.Fatalf("overlap after write rel=%s cpus=%s: kubepods=%s kubesandbox=%s overlap=%s writes=%#v",
				rel, data.CPUs, cg.cpus["kubepods"].String(), cg.cpus["kubesandbox"].String(), overlap.String(), cg.writes)
		}
	}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}

	wantPrefix := []cpusetWrite{
		{rel: "kubepods", cpus: "0-2"},
		{rel: "kubesandbox", cpus: "3-5"},
		{rel: "kubepods", cpus: "0-2,6"},
		{rel: "kubesandbox", cpus: "3-5,99"},
	}
	if len(cg.writes) < len(wantPrefix) {
		t.Fatalf("writes = %#v, want prefix %#v", cg.writes, wantPrefix)
	}
	if !reflect.DeepEqual(cg.writes[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("writes = %#v, want prefix %#v", cg.writes, wantPrefix)
	}
}

func TestApplyDAGDiffGuardsSiblingGrowWhenSourceShrinkFails(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 2)},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4, 5, 99)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2, 99)
	cg.cpus["kubesandbox"] = machine.NewCPUSet(3, 4, 5)
	cg.applyErr["kubepods"] = syscall.EBUSY
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		overlap := cg.cpus["kubepods"].Intersection(cg.cpus["kubesandbox"])
		if !overlap.IsEmpty() {
			t.Fatalf("overlap after write rel=%s cpus=%s: kubepods=%s kubesandbox=%s overlap=%s writes=%#v",
				rel, data.CPUs, cg.cpus["kubepods"].String(), cg.cpus["kubesandbox"].String(), overlap.String(), cg.writes)
		}
	}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err == nil {
		t.Fatalf("expected source shrink error, got nil; writes=%#v", cg.writes)
	}
	for _, w := range cg.writes {
		if w.rel == "kubesandbox" && strings.Contains(w.cpus, "99") {
			t.Fatalf("target sibling should not grow failed CPU 99; writes=%#v", cg.writes)
		}
	}
}

func TestApplyDAGDiffPreShrinksReclaimBeforePendingPrimaryGrow(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 2)},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4, 5, 6)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2)
	cg.cpus["kubesandbox"] = machine.NewCPUSet(3, 4, 5, 6)
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		overlap := cg.cpus["kubepods"].Intersection(cg.cpus["kubesandbox"])
		if !overlap.IsEmpty() {
			t.Fatalf("overlap after write rel=%s cpus=%s: kubepods=%s kubesandbox=%s overlap=%s writes=%#v",
				rel, data.CPUs, cg.cpus["kubepods"].String(), cg.cpus["kubesandbox"].String(), overlap.String(), cg.writes)
		}
	}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:                    dag,
		Cgroup:                 cg,
		ProtectedPendingCPUSet: machine.NewCPUSet(6),
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}

	wantPrefix := []cpusetWrite{
		{rel: "kubesandbox", cpus: "3-5"},
		{rel: "kubepods", cpus: "0-2,6"},
	}
	if len(cg.writes) < len(wantPrefix) {
		t.Fatalf("writes = %#v, want prefix %#v", cg.writes, wantPrefix)
	}
	if !reflect.DeepEqual(cg.writes[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("writes = %#v, want prefix %#v", cg.writes, wantPrefix)
	}
}

func TestApplyDAGDiffDoesNotWriteEmptyV1PreShrinkOrGrowFailedCPU(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet()},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(6)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(6)
	cg.cpus["kubesandbox"] = machine.NewCPUSet()
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		if data.CPUs == "" {
			t.Fatalf("cgroup v1 should not write empty cpuset during pre-shrink; writes=%#v", cg.writes)
		}
		overlap := cg.cpus["kubepods"].Intersection(cg.cpus["kubesandbox"])
		if !overlap.IsEmpty() {
			t.Fatalf("overlap after write rel=%s cpus=%s: kubepods=%s kubesandbox=%s overlap=%s writes=%#v",
				rel, data.CPUs, cg.cpus["kubepods"].String(), cg.cpus["kubesandbox"].String(), overlap.String(), cg.writes)
		}
	}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff should skip unsafe empty v1 pre-shrink without failing: %v; writes=%#v", err, cg.writes)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("failed empty pre-shrink should guard target grow, got writes=%#v", cg.writes)
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

func TestApplyDAGDiffExpandsEmptyTargetsWithProtectKubeLeafV2(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet()}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.version = cgroupclient.CgroupVersionV2
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1)
	cg.cpus["kubepods/burstable"] = machine.NewCPUSet(0, 1)
	cg.cpus["kubepods/burstable/pod-a"] = machine.NewCPUSet(0, 1)
	cg.cpus["kubepods/burstable/pod-a/container-a"] = machine.NewCPUSet(0, 1)
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-a"}
	cg.children["kubepods/burstable/pod-a"] = []string{"container-a"}

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:              dag,
		Cgroup:           cg,
		SkipObservedRead: true,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if res.Applied == 0 {
		t.Fatalf("expected empty target writes with protect enabled, got %+v", res)
	}
	wantCPUSetByRel := map[string]string{
		"kubepods":                             "",
		"kubepods/burstable":                   "",
		"kubepods/burstable/pod-a":             "",
		"kubepods/burstable/pod-a/container-a": "",
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

func TestApplyDAGDiffConvergesExistingKubeLeavesBeforePrimaryShrink(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2)},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4, 5)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	full := machine.NewCPUSet(0, 1, 2, 3, 4, 5)
	cg.cpus["kubepods"] = full.Clone()
	cg.cpus["kubepods/burstable"] = full.Clone()
	cg.cpus["kubepods/burstable/pod-abc"] = full.Clone()
	cg.cpus["kubepods/burstable/pod-abc/container-a"] = full.Clone()
	cg.cpus["kubesandbox"] = full.Clone()
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-abc"}
	cg.children["kubepods/burstable/pod-abc"] = []string{"container-a"}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	for _, rel := range []string{
		"kubepods",
		"kubepods/burstable",
		"kubepods/burstable/pod-abc",
		"kubepods/burstable/pod-abc/container-a",
	} {
		if got := cg.cpus[rel].String(); got != "1-2" {
			t.Fatalf("cpuset @ %s = %s, want 1-2; writes=%#v", rel, got, cg.writes)
		}
	}
	if got := cg.cpus["kubesandbox"].String(); got != "3-5" {
		t.Fatalf("reclaim cpuset = %s, want 3-5; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffExpandsKubeIntermediateBeforeConvergingLiveLeaves(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2, 3)},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(4, 5)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubepods"] = machine.NewCPUSet(1, 2, 3, 4, 5)
	cg.cpus["kubepods/burstable"] = machine.NewCPUSet(1, 4, 5)
	cg.cpus["kubepods/burstable/pod-abc"] = machine.NewCPUSet(1, 4, 5)
	cg.cpus["kubepods/burstable/pod-abc/container-a"] = machine.NewCPUSet(1, 4, 5)
	cg.cpus["kubesandbox"] = machine.NewCPUSet(4, 5)
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-abc"}
	cg.children["kubepods/burstable/pod-abc"] = []string{"container-a"}
	cg.files["kubepods/burstable/pod-abc/container-a"] = map[string][]byte{"tasks": []byte("123\n")}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	for _, rel := range []string{
		"kubepods",
		"kubepods/burstable",
		"kubepods/burstable/pod-abc",
		"kubepods/burstable/pod-abc/container-a",
	} {
		if got := cg.cpus[rel].String(); got != "1-3" {
			t.Fatalf("cpuset @ %s = %s, want 1-3; writes=%#v", rel, got, cg.writes)
		}
	}
	expandedPod := false
	for _, w := range cg.writes {
		if w.rel == "kubepods/burstable/pod-abc" && w.cpus == "1-5" {
			expandedPod = true
		}
	}
	if !expandedPod {
		t.Fatalf("expected pod intermediate to be expanded before leaf converge; writes=%#v", cg.writes)
	}
}

func TestApplyDAGDiffConvergesUnmanagedKubePodLeaf(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0)
	cg.cpus["kubepods/burstable/pod-abc/container-a"] = machine.NewCPUSet(5, 6)
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-abc"}
	cg.children["kubepods/burstable/pod-abc"] = []string{"container-a"}

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if got := cg.cpus["kubepods"].String(); got != "0-1" {
		t.Fatalf("primary target = %s, want 0-1 without unmanaged leaf widening; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "0-1" {
		t.Fatalf("unmanaged leaf cpuset = %s, want converged 0-1; writes=%#v", got, cg.writes)
	}
	wroteLeaf := false
	for _, w := range cg.writes {
		if w.rel == "kubepods/burstable/pod-abc/container-a" {
			wroteLeaf = true
		}
	}
	if !wroteLeaf {
		t.Fatalf("unmanaged leaf should be written; writes=%#v", cg.writes)
	}
	_ = res
}

func TestApplyDAGDiffConvergesUnmanagedKubePauseLeaf(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0)
	cg.cpus["kubepods/besteffort/pod-abc"] = machine.NewCPUSet(5, 6)
	cg.children["kubepods"] = []string{"besteffort"}
	cg.children["kubepods/besteffort"] = []string{"pod-abc"}

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if got := cg.cpus["kubepods"].String(); got != "0-1" {
		t.Fatalf("primary target = %s, want 0-1 without unmanaged pause widening; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubepods/besteffort/pod-abc"].String(); got != "0-1" {
		t.Fatalf("unmanaged pause leaf cpuset = %s, want converged 0-1; writes=%#v", got, cg.writes)
	}
	wroteLeaf := false
	for _, w := range cg.writes {
		if w.rel == "kubepods/besteffort/pod-abc" {
			wroteLeaf = true
		}
	}
	if !wroteLeaf {
		t.Fatalf("unmanaged pause leaf should be written; writes=%#v", cg.writes)
	}
	_ = res
}

// TestApplyDAGDiffProtectStillWritesExpectedLeaf verifies protection does not
// suppress writes for container leaves that ARE present in ExpectedCPUSetByRel:
// those still get their resolved allocation enforced.
func TestApplyDAGDiffProtectStillWritesExpectedLeaf(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 2)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0)
	cg.cpus["kubepods/burstable"] = machine.NewCPUSet(0)
	cg.cpus["kubepods/burstable/pod-abc"] = machine.NewCPUSet(0)
	cg.cpus["kubepods/burstable/pod-abc/container-a"] = machine.NewCPUSet(0)
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-abc"}
	cg.children["kubepods/burstable/pod-abc"] = []string{"container-a"}

	if _, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"kubepods/burstable/pod-abc/container-a": machine.NewCPUSet(1, 2),
		},
	}); err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "1-2" {
		t.Fatalf("expected leaf cpuset = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	for _, rel := range []string{"kubepods/burstable", "kubepods/burstable/pod-abc"} {
		if got := cg.cpus[rel].String(); got != "0-2" {
			t.Fatalf("protected intermediate %s cpuset = %s, want 0-2; writes=%#v", rel, got, cg.writes)
		}
	}
}

// TestApplyDAGDiffReleasesUnmanagedLeafWithoutProtect verifies the reset/widen
// path (protection disabled) still propagates the parent target onto an
// unmanaged leaf, which is how a polluted leaf recovers back to a wide cpuset.
func TestApplyDAGDiffReleasesUnmanagedLeafWithoutProtect(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5, 6)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0)
	cg.cpus["kubepods/burstable/pod-abc/container-a"] = machine.NewCPUSet(5)
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-abc"}
	cg.children["kubepods/burstable/pod-abc"] = []string{"container-a"}

	if _, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	}); err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "0-6" {
		t.Fatalf("widened leaf cpuset = %s, want 0-6; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffConvergesLiveDisjointChildBeforeParentShrink(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1, 2, 3)
	// A live child sits on a cpuset disjoint from the new parent target {0,1}.
	// Direct descendant convergence first parks it inside the parent target, so
	// the parent can shrink without relying on kube-specific path parsing.
	cg.cpus["primary/pod-x"] = machine.NewCPUSet(7, 8)
	cg.children["primary"] = []string{"pod-x"}
	cg.files["primary/pod-x"] = map[string][]byte{"tasks": []byte("123\n")}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["primary/pod-x"].String(); got != "0-1" {
		t.Fatalf("child cpuset = %s, want 0-1; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffParksEmptyDisjointChildBeforeParentShrink(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox"] = machine.NewCPUSet(2, 3, 4, 5)
	cg.cpus["kubesandbox/reclaimed-0"] = machine.NewCPUSet(4, 5)
	cg.children["kubesandbox"] = []string{"reclaimed-0"}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err != nil {
		t.Fatalf("empty disjoint child should be parked before parent shrink; err=%v writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubesandbox/reclaimed-0"].String(); got != "2-3" {
		t.Fatalf("stale child cpuset = %s, want parked inside parent 2-3; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubesandbox"].String(); got != "2-3" {
		t.Fatalf("parent cpuset = %s, want 2-3; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffShrinkFallbackRelistsLiveChildrenAfterCacheMiss(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1, 2, 3)

	createdLateChild := false
	cg.onApply = func(rel string, data *cgcommon.CPUSetData) {
		if rel != "primary" || data.CPUs != "0-1" || createdLateChild {
			return
		}
		createdLateChild = true
		cg.children["primary"] = []string{"late-child"}
		cg.cpus["primary/late-child"] = machine.NewCPUSet(0, 1, 2, 3)
	}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["primary/late-child"].String(); got != "0-1" {
		t.Fatalf("late child cpuset = %s, want 0-1; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["primary"].String(); got != "0-1" {
		t.Fatalf("primary cpuset = %s, want 0-1; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffConvergesExpectedLeafCurrent(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(1, 2, 3)
	cg.cpus["kubepods/burstable/pod-abc/container-a"] = machine.NewCPUSet(3, 4)
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-abc"}
	cg.children["kubepods/burstable/pod-abc"] = []string{"container-a"}

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"kubepods/burstable/pod-abc/container-a": machine.NewCPUSet(1, 2),
		},
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if got := cg.cpus["kubepods"].String(); got != "1-2" {
		t.Fatalf("primary effective target = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "1-2" {
		t.Fatalf("expected leaf cpuset = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	_ = res
}

func TestComputeEffectiveTargetsDoesNotProtectPodParentOrSandboxFullCPUSet(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	effective, err := computeEffectiveTargets(dag, false, machine.NewCPUSet())
	if err != nil {
		t.Fatalf("computeEffectiveTargets: %v", err)
	}
	if got := effective["kubepods"].String(); got != "1-2" {
		t.Fatalf("primary effective target = %s, want desired 1-2 without current leaf widening", got)
	}
}

// TestApplyDAGDiffWidensPrimaryEffectiveTargetForPendingAllocation verifies that
// an admit-window container (allocation known, no cgroup leaf yet) folded in via
// ProtectedPendingCPUSet also widens the primary effective target, so the parent
// never shrinks below an allocation that is about to materialize.
func TestApplyDAGDiffWidensPrimaryEffectiveTargetForPendingAllocation(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(1, 2, 9)

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:                    dag,
		Cgroup:                 cg,
		ProtectedPendingCPUSet: machine.NewCPUSet(9),
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if got := cg.cpus["kubepods"].String(); got != "1-2,9" {
		t.Fatalf("primary effective target = %s, want 1-2,9 (pending folded in); writes=%#v", got, cg.writes)
	}
	_ = res
}

// TestApplyDAGDiffDeductsPrimaryEffectiveCPUsFromReclaim verifies that boundary
// CPUs held by the primary effective target are removed from reclaim targets
// before applying, keeping partitions disjoint without rejecting a recoverable
// transient overlap.
func TestApplyDAGDiffConvergesExpectedLeafWithoutDeductingReclaim(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4, 5)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(1, 2, 3)
	cg.cpus["reclaim"] = machine.NewCPUSet(3, 4, 5)
	// expected leaf currently sits on cpu 3, which belongs to the reclaim partition,
	// but it should be converged to the primary target instead of widening primary.
	cg.cpus["kubepods/burstable/pod-abc/container-a"] = machine.NewCPUSet(3)
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-abc"}
	cg.children["kubepods/burstable/pod-abc"] = []string{"container-a"}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"kubepods/burstable/pod-abc/container-a": machine.NewCPUSet(1, 2),
		},
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubepods"].String(); got != "1-2" {
		t.Fatalf("primary target = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["reclaim"].String(); got != "3-5" {
		t.Fatalf("reclaim target = %s, want 3-5 without current leaf deduction; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffWritesEmptyCPUSetOnCgroupV2(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet()},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.version = cgroupclient.CgroupVersionV2
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1)

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	if len(cg.writes) != 1 {
		t.Fatalf("writes = %#v, want one empty cpuset write", cg.writes)
	}
	write := cg.writes[0]
	if write.rel != "kubepods" || write.cpus != "" || !write.writeEmptyCPUs {
		t.Fatalf("v2 empty cpuset write = %#v, want rel=kubepods cpus empty with WriteEmptyCPUs", write)
	}
}

func TestApplyDAGDiffAllowsReclaimNUMABucketDisjointReplacementWhenParentContainsTarget(t *testing.T) {
	t.Parallel()

	parentCPUs := machine.NewCPUSet(42, 43, 44, 45, 46, 47, 95, 96, 138, 139, 140, 141, 142, 143, 144, 191)
	numa0CPUs := machine.NewCPUSet(42, 43, 44, 45, 46, 47, 96, 138, 139, 140, 141, 142, 143)
	numa1CPUs := machine.NewCPUSet(95, 144, 191)
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: parentCPUs},
		{Rel: "kubesandbox/numa0", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: numa0CPUs},
		{Rel: "kubesandbox/numa1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: numa1CPUs},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox"] = parentCPUs.Clone()
	cg.cpus["kubesandbox/numa0"] = numa0CPUs.Clone()
	cg.cpus["kubesandbox/numa1"] = numa0CPUs.Clone()
	cg.children["kubesandbox"] = []string{"numa0", "numa1"}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubesandbox/numa1"].String(); got != numa1CPUs.String() {
		t.Fatalf("numa1 target = %s, want %s; writes=%#v", got, numa1CPUs.String(), cg.writes)
	}
	if overlap := cg.cpus["kubesandbox/numa0"].Intersection(cg.cpus["kubesandbox/numa1"]); !overlap.IsEmpty() {
		t.Fatalf("reclaim NUMA buckets overlap after apply: overlap=%s writes=%#v", overlap.String(), cg.writes)
	}
}

func TestApplyDAGDiffRejectsReclaimNUMABucketDisjointReplacementWithoutReclaimParent(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox/numa1", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(4, 5)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox/numa1"] = machine.NewCPUSet(1, 2)

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err == nil || !strings.Contains(err.Error(), "disjoint cpuset change") {
		t.Fatalf("expected disjoint replacement rejection, got err=%v writes=%#v", err, cg.writes)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("disjoint validation should fail before writes, got %#v", cg.writes)
	}
}

func TestApplyDAGDiffRejectsReclaimNUMABucketSiblingOverlap(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1, 2, 3)},
		{Rel: "kubesandbox/numa0", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(1, 2)},
		{Rel: "kubesandbox/numa1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2, 3)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox"] = machine.NewCPUSet(1, 2, 3)
	cg.cpus["kubesandbox/numa0"] = machine.NewCPUSet(1, 2)
	cg.cpus["kubesandbox/numa1"] = machine.NewCPUSet(3)

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err == nil || !strings.Contains(err.Error(), "reclaim numa bucket overlap") {
		t.Fatalf("expected reclaim numa bucket overlap error, got err=%v writes=%#v", err, cg.writes)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("overlap validation should fail before writes, got %#v", cg.writes)
	}
}

func TestApplyDAGDiffWidensReclaimParentToContainNUMABucket(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1)},
		{Rel: "kubesandbox/numa0", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(1, 2)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox"] = machine.NewCPUSet(1)
	cg.cpus["kubesandbox/numa0"] = machine.NewCPUSet(1)
	cg.children["kubesandbox"] = []string{"numa0"}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubesandbox"].String(); got != "1-2" {
		t.Fatalf("reclaim parent target = %s, want 1-2 containing bucket; writes=%#v", got, cg.writes)
	}
}

// TestApplyDAGDiffShrinkBlockerCurrentOutsideReason verifies the
// current_outside_parent reason: a child whose cpuset overlaps but is not fully
// inside the new parent target (and has no expected entry) is reported with the
// current_outside_parent reason rather than being mislabeled expected_outside.
func TestApplyDAGDiffShrinkBlockerCurrentOutsideReason(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1, 2, 3)
	// child overlaps {1} but also holds {5}, so it straddles the new parent {0,1}.
	cg.cpus["primary/pod-y"] = machine.NewCPUSet(1, 5)
	cg.children["primary"] = []string{"pod-y"}
	// force the child clamp to fail so the blocker diagnostics are produced.
	cg.failRel["primary/pod-y"] = true

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err == nil {
		t.Fatalf("expected shrink blocked error, got nil; writes=%#v", cg.writes)
	}
	if !strings.Contains(err.Error(), "current_outside_parent") {
		t.Fatalf("shrink blocker error missing current_outside_parent; got %q", err.Error())
	}
}

func TestApplyDAGDiffReportsNonStaleShrinkBlockers(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "system", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["system"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["system/legacy"] = machine.NewCPUSet(1, 2)
	cg.children["system"] = []string{"legacy"}
	cg.failRel["system/legacy"] = true

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err == nil {
		t.Fatalf("expected non-stale shrink blocker error, got nil; writes=%#v", cg.writes)
	}
	for _, want := range []string{
		"shrink converge exhausted @ system",
		"rel=system/legacy",
		"reason=current_outside_parent",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("non-stale shrink blocker error missing %q; got %q", want, err.Error())
		}
	}
}

func TestApplyDAGDiffReturnsErrorWhenKubePodLeafConvergeFailsDuringShrink(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["kubepods/podabc123"] = machine.NewCPUSet(1, 2)
	cg.cpus["kubepods/poddef456"] = machine.NewCPUSet(0, 3)
	cg.children["kubepods"] = []string{"podabc123", "poddef456"}
	cg.failRel["kubepods/podabc123"] = true
	cg.failRel["kubepods/poddef456"] = true

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err == nil {
		t.Fatalf("expected stale pod cgroup converge failure to block shrink; result=%+v writes=%#v", res, cg.writes)
	}
	if !strings.Contains(err.Error(), "shrink converge") {
		t.Fatalf("error = %v, want shrink converge failure", err)
	}
	if res.Failed == 0 {
		t.Fatalf("result=%+v, want failed child convergence counted", res)
	}
}

func TestApplyDAGDiffConvergesStaleResidualWithoutDeductingReclaim(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["kubepods/podabc123"] = machine.NewCPUSet(1, 2)
	cg.cpus["kubesandbox"] = machine.NewCPUSet(2, 3)
	cg.children["kubepods"] = []string{"podabc123"}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubepods"].String(); got != "0-1" {
		t.Fatalf("primary target = %s, want 0-1; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubepods/podabc123"].String(); got != "0-1" {
		t.Fatalf("stale pod cpuset = %s, want converged 0-1; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubesandbox"].String(); got != "2-3" {
		t.Fatalf("reclaim target = %s, want unchanged 2-3; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffDoesNotWidenEmptyPrimaryTargetForStaleResidualOnCgroupV2(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.version = cgroupclient.CgroupVersionV2
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["kubepods/podabc123"] = machine.NewCPUSet(1, 2)
	cg.children["kubepods"] = []string{"podabc123"}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubepods"].String(); got != "" {
		t.Fatalf("v2 empty primary target = %q, want empty inheritance target; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffConvergesPrimaryAndStaleResidual(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["kubepods/podabc123"] = machine.NewCPUSet(1, 2)
	cg.children["kubepods"] = []string{"podabc123"}

	if _, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	}); err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubepods"].String(); got != "0-1" {
		t.Fatalf("primary target = %s, want 0-1; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubepods/podabc123"].String(); got != "0-1" {
		t.Fatalf("stale pod target = %s, want 0-1; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffConvergesStaleReclaimSandboxWithoutOverlappingNUMABuckets(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0), Mems: "0"},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1, 2, 3, 4), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-0", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(1, 2), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(3, 4), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	fullReclaim := machine.NewCPUSet(1, 2, 3, 4)
	cg.cpus["kubepods"] = machine.NewCPUSet(0)
	cg.cpus["kubesandbox"] = fullReclaim.Clone()
	cg.cpus["kubesandbox/reclaimed-0"] = fullReclaim.Clone()
	cg.cpus["kubesandbox/reclaimed-0/sandbox-stale-a"] = fullReclaim.Clone()
	cg.cpus["kubesandbox/reclaimed-1"] = fullReclaim.Clone()
	cg.cpus["kubesandbox/reclaimed-1/sandbox-stale-b"] = fullReclaim.Clone()
	cg.children["kubesandbox"] = []string{"reclaimed-0", "reclaimed-1"}
	cg.children["kubesandbox/reclaimed-0"] = []string{"sandbox-stale-a"}
	cg.children["kubesandbox/reclaimed-1"] = []string{"sandbox-stale-b"}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubesandbox/reclaimed-0"].String(); got != "1-2" {
		t.Fatalf("reclaimed-0 = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubesandbox/reclaimed-0/sandbox-stale-a"].String(); got != "1-2" {
		t.Fatalf("stale sandbox a = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubesandbox/reclaimed-1"].String(); got != "3-4" {
		t.Fatalf("reclaimed-1 = %s, want 3-4; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubesandbox/reclaimed-1/sandbox-stale-b"].String(); got != "3-4" {
		t.Fatalf("stale sandbox b = %s, want 3-4; writes=%#v", got, cg.writes)
	}
	if overlap := cg.cpus["kubesandbox/reclaimed-0"].Intersection(cg.cpus["kubesandbox/reclaimed-1"]); !overlap.IsEmpty() {
		t.Fatalf("reclaim NUMA buckets overlap: %s; writes=%#v", overlap.String(), cg.writes)
	}
}

func TestApplyDAGDiffReturnsErrorWhenStaleReclaimSandboxConvergeFails(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-0", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1)
	cg.cpus["kubesandbox"] = machine.NewCPUSet(2, 3, 4)
	cg.cpus["kubesandbox/reclaimed-0"] = machine.NewCPUSet(2)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(3, 4)
	cg.cpus["kubesandbox/reclaimed-1/sandbox-stale"] = machine.NewCPUSet(4)
	cg.children["kubesandbox"] = []string{"reclaimed-0", "reclaimed-1"}
	cg.children["kubesandbox/reclaimed-1"] = []string{"sandbox-stale"}
	cg.failRel["kubesandbox/reclaimed-1/sandbox-stale"] = true

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err == nil {
		t.Fatalf("expected stale reclaim sandbox converge failure; result=%+v writes=%#v", res, cg.writes)
	}
	if !strings.Contains(err.Error(), "shrink blocked") {
		t.Fatalf("error = %v, want shrink blocked", err)
	}
}

func TestApplyDAGDiffConvergesStaleReclaimSandboxWhenItOverlapsPrimary(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 4), Mems: "0"},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-0", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 4)
	cg.cpus["kubesandbox"] = machine.NewCPUSet(2, 3, 4)
	cg.cpus["kubesandbox/reclaimed-0"] = machine.NewCPUSet(2)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(3, 4)
	cg.cpus["kubesandbox/reclaimed-1/sandbox-stale"] = machine.NewCPUSet(4)
	cg.children["kubesandbox"] = []string{"reclaimed-0", "reclaimed-1"}
	cg.children["kubesandbox/reclaimed-1"] = []string{"sandbox-stale"}

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err != nil {
		t.Fatalf("stale reclaim sandbox must not block primary admission; err=%v result=%+v writes=%#v", err, res, cg.writes)
	}
	if !machine.NewCPUSet(4).IsSubsetOf(cg.cpus["kubepods"]) {
		t.Fatalf("primary target must keep active CPU even if a stale reclaim sandbox still holds it; kubepods=%s writes=%#v",
			cg.cpus["kubepods"].String(), cg.writes)
	}
	if got := cg.cpus["kubesandbox/reclaimed-1"].String(); got != "3" {
		t.Fatalf("reclaim bucket = %s, want 3; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubesandbox/reclaimed-1/sandbox-stale"].String(); got != "3" {
		t.Fatalf("stale sandbox = %s, want converged 3; writes=%#v", got, cg.writes)
	}
}

func TestApplyDAGDiffReturnsErrorWhenStaleReclaimSandboxConvergeFailsAfterPrimaryDeduction(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 4), Mems: "0"},
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3, 4), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-0", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2), Mems: "0"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(3, 4), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 4)
	cg.cpus["kubesandbox"] = machine.NewCPUSet(2, 3, 4)
	cg.cpus["kubesandbox/reclaimed-0"] = machine.NewCPUSet(2)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(3, 4)
	cg.cpus["kubesandbox/reclaimed-1/sandbox-stale"] = machine.NewCPUSet(4)
	cg.children["kubesandbox"] = []string{"reclaimed-0", "reclaimed-1"}
	cg.children["kubesandbox/reclaimed-1"] = []string{"sandbox-stale"}
	cg.failRel["kubesandbox/reclaimed-1/sandbox-stale"] = true

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err == nil {
		t.Fatalf("expected stale reclaim sandbox converge failure after primary deduction; result=%+v writes=%#v", res, cg.writes)
	}
	if !strings.Contains(err.Error(), "shrink blocked") {
		t.Fatalf("error = %v, want shrink blocked", err)
	}
}

// TestApplyDAGDiffWriteAndDescendStopsOnApplyFailure verifies that when
// expandDescendants fails to apply a cpuset at some intermediate rel, it does
// not continue writing further descendants under that failed parent. Otherwise
// TestApplyDAGDiffWriteAndDescendSurfacesApplyFailure verifies that a
// descendant convergence failure is reported to the caller.
func TestApplyDAGDiffWriteAndDescendSurfacesApplyFailure(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"burstable"}
	cg.children["primary/burstable"] = []string{"leaf"}
	// The middle intermediate write fails; direct descendant convergence reports
	// the error while still allowing lower descendants to converge first.
	cg.failRel["primary/burstable"] = true

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err == nil {
		t.Fatalf("expected apply error from failed intermediate write, got nil; writes=%#v", cg.writes)
	}
}

func TestApplyDAGDiffSkipsDisappearedDynamicChildDuringExpand(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"sandbox-a"}
	cg.children["primary/sandbox-a"] = []string{"kata-a"}
	cg.applyErr["primary/sandbox-a/kata-a"] = os.ErrNotExist

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; result=%+v writes=%#v", err, res, cg.writes)
	}
	if res.Skipped == 0 {
		t.Fatalf("result = %+v, want disappeared dynamic child counted as skipped", res)
	}
	if res.Failed != 0 {
		t.Fatalf("result = %+v, want no failed writes for disappeared dynamic child", res)
	}
}

func TestApplyDAGDiffSkipsDisappearedDynamicIntermediate(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"sandbox-a"}
	cg.children["primary/sandbox-a"] = []string{"kata-a"}
	cg.applyErr["primary/sandbox-a"] = os.ErrNotExist

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; result=%+v writes=%#v", err, res, cg.writes)
	}
	if res.Skipped == 0 {
		t.Fatalf("result = %+v, want disappeared dynamic intermediate counted as skipped", res)
	}
}

func TestApplyDAGDiffSkipsDisappearedExpectedLeafDuringExpand(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	expectedRel := "kubepods/burstable/pod-a/container-a"
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1)
	cg.cpus["kubepods/burstable"] = machine.NewCPUSet(0, 1)
	cg.cpus["kubepods/burstable/pod-a"] = machine.NewCPUSet(0, 1)
	cg.cpus[expectedRel] = machine.NewCPUSet(0)
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-a"}
	cg.children["kubepods/burstable/pod-a"] = []string{"container-a"}
	cg.applyErr[expectedRel] = os.ErrNotExist

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			expectedRel: machine.NewCPUSet(1),
		},
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v; result=%+v writes=%#v", err, res, cg.writes)
	}
	if res.Skipped == 0 {
		t.Fatalf("result = %+v, want disappeared expected leaf counted as skipped", res)
	}
	if res.Failed != 0 {
		t.Fatalf("result = %+v, want no failed writes for disappeared expected leaf", res)
	}
}

func TestApplyDAGDiffReturnsNonNotFoundDynamicChildError(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"child-a"}
	cg.applyErr["primary/child-a"] = syscall.EINVAL

	res, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err == nil {
		t.Fatalf("ApplyDAGDiff returned nil for non-not-found error; result=%+v writes=%#v", res, cg.writes)
	}
	if res.Failed == 0 {
		t.Fatalf("result = %+v, want failed write for non-not-found error", res)
	}
}

func TestIsCgroupNotFoundError(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		os.ErrNotExist,
		syscall.ENOTDIR,
		fmt.Errorf("wrapped: %w", os.ErrNotExist),
		fmt.Errorf("openat2 cpuset.cpus: no such file or directory"),
		fmt.Errorf("openat2 parent: not a directory"),
	} {
		if !isCgroupNotFoundError(err) {
			t.Fatalf("isCgroupNotFoundError(%v) = false, want true", err)
		}
	}
	if isCgroupNotFoundError(syscall.EINVAL) {
		t.Fatalf("isCgroupNotFoundError(EINVAL) = true, want false")
	}
}

// TestApplyDAGDiffExpandStopsOnNodeGrowFailure verifies that when a controlled
// node's own grow write fails, ApplyDAGDiff does not descend into its subtree.
// Otherwise descendants would be written to the (larger) effective target while
// the node itself is still at the smaller observed cpuset, violating the cgroup
// v1 parent-superset invariant.
func TestApplyDAGDiffExpandStopsOnNodeGrowFailure(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	// primary is empty -> grow to {0,1}; its own write fails.
	cg.children["primary"] = []string{"leaf"}
	cg.failRel["primary"] = true

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err == nil {
		t.Fatalf("expected apply error from failed node grow, got nil; writes=%#v", cg.writes)
	}
	for _, w := range cg.writes {
		if w.rel == "primary/leaf" {
			t.Fatalf("descendant must NOT be written after node grow failure; writes=%#v", cg.writes)
		}
	}
}
