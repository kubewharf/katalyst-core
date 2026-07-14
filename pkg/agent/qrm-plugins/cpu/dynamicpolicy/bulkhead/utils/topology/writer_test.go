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
	"strings"
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
	onApply  func(rel string, data *cgcommon.CPUSetData)
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
	if f.onApply != nil {
		f.onApply(rel, data)
	}
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
		DAG:                         dag,
		Cgroup:                      cg,
		SkipObservedRead:            true,
		ProtectUnmanagedKubePodLeaf: true,
		KubeManagedRelPrefix:        "kubepods",
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

// TestApplyDAGDiffProtectsUnmanagedKubePodLeaf reproduces the production
// pollution scenario: a live container leaf under kubepods that is NOT present
// in ExpectedCPUSetByRel must NOT inherit the (transient) parent pool target
// when protection is enabled. This is the direct fix for the cold-start seed
// pollution that later blocked kubepods from shrinking.
func TestApplyDAGDiffProtectsUnmanagedKubePodLeaf(t *testing.T) {
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
		DAG:                         dag,
		Cgroup:                      cg,
		ProtectUnmanagedKubePodLeaf: true,
		KubeManagedRelPrefix:        "kubepods",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	// The unmanaged live leaf keeps its own cpuset; the parent target is not
	// propagated onto it.
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "5-6" {
		t.Fatalf("protected leaf cpuset = %s, want unchanged 5-6; writes=%#v", got, cg.writes)
	}
	for _, w := range cg.writes {
		if w.rel == "kubepods/burstable/pod-abc/container-a" {
			t.Fatalf("protected leaf must not be written, got write=%#v", w)
		}
	}
	if res.Skipped == 0 {
		t.Fatalf("expected protected-leaf skip to be counted, got %+v", res)
	}
}

// TestApplyDAGDiffProtectsUnmanagedKubePauseLeaf verifies that the protection
// also covers the pod cgroup itself when it is the effective leaf, which can be
// the pause/sandbox container's cgroup in some runtimes/layouts.
func TestApplyDAGDiffProtectsUnmanagedKubePauseLeaf(t *testing.T) {
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
		DAG:                         dag,
		Cgroup:                      cg,
		ProtectUnmanagedKubePodLeaf: true,
		KubeManagedRelPrefix:        "kubepods",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if got := cg.cpus["kubepods/besteffort/pod-abc"].String(); got != "5-6" {
		t.Fatalf("protected pause leaf cpuset = %s, want unchanged 5-6; writes=%#v", got, cg.writes)
	}
	for _, w := range cg.writes {
		if w.rel == "kubepods/besteffort/pod-abc" {
			t.Fatalf("protected pause leaf must not be written, got write=%#v", w)
		}
	}
	if res.Skipped == 0 {
		t.Fatalf("expected protected pause leaf skip to be counted, got %+v", res)
	}
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
		DAG:                         dag,
		Cgroup:                      cg,
		ProtectUnmanagedKubePodLeaf: true,
		KubeManagedRelPrefix:        "kubepods",
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
		DAG:                         dag,
		Cgroup:                      cg,
		ProtectUnmanagedKubePodLeaf: false,
	}); err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "0-6" {
		t.Fatalf("widened leaf cpuset = %s, want 0-6; writes=%#v", got, cg.writes)
	}
}

// TestApplyDAGDiffShrinkBlockerDiagnostics verifies that when a shrink is
// blocked by a disjoint child, the error surfaces the blocking rel and its
// current cpuset (and stops retrying early).
func TestApplyDAGDiffShrinkBlockerDiagnostics(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1, 2, 3)
	// live child sits on a cpuset disjoint from the new parent target {0,1}.
	cg.cpus["primary/pod-x"] = machine.NewCPUSet(7, 8)
	cg.children["primary"] = []string{"pod-x"}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err == nil {
		t.Fatalf("expected shrink blocked error, got nil; writes=%#v", cg.writes)
	}
	msg := err.Error()
	for _, want := range []string{"primary/pod-x", "current_disjoint_parent", "7-8"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("shrink blocker error missing %q; got %q", want, msg)
		}
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
		DAG:                         dag,
		Cgroup:                      cg,
		Mems:                        "0",
		ProtectUnmanagedKubePodLeaf: true,
		KubeManagedRelPrefix:        "primary",
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

// TestApplyDAGDiffWidensPrimaryEffectiveTargetForProtectedLeaf reproduces the
// production seedpool pollution: kubepods desired shrinks to {1,2} while a live
// unmanaged container leaf still sits on {73,79} (disjoint from the desired
// target). With protection on, the primary effective target must be widened to
// desired ∪ protectedLeafCurrent so the parent write does NOT try to shrink
// below the live leaf (which cgroup v1 would reject), and the leaf itself must
// stay untouched.
func TestApplyDAGDiffWidensPrimaryEffectiveTargetForProtectedLeaf(t *testing.T) {
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
		DAG:                         dag,
		Cgroup:                      cg,
		ProtectUnmanagedKubePodLeaf: true,
		KubeManagedRelPrefix:        "kubepods",
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	// Effective target = desired {1,2} ∪ protected leaf current {3,4} = {1,2,3,4}.
	if got := cg.cpus["kubepods"].String(); got != "1-4" {
		t.Fatalf("primary effective target = %s, want widened 1-4; writes=%#v", got, cg.writes)
	}
	// The protected leaf must not be written / shrunk.
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "3-4" {
		t.Fatalf("protected leaf cpuset = %s, want unchanged 3-4; writes=%#v", got, cg.writes)
	}
	for _, w := range cg.writes {
		if w.rel == "kubepods/burstable/pod-abc/container-a" {
			t.Fatalf("protected leaf must not be written, got write=%#v", w)
		}
	}
	_ = res
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
		DAG:                         dag,
		Cgroup:                      cg,
		ProtectUnmanagedKubePodLeaf: true,
		KubeManagedRelPrefix:        "kubepods",
		ProtectedPendingCPUSet:      machine.NewCPUSet(9),
	})
	if err != nil {
		t.Fatalf("ApplyDAGDiff: %v", err)
	}
	if got := cg.cpus["kubepods"].String(); got != "1-2,9" {
		t.Fatalf("primary effective target = %s, want 1-2,9 (pending folded in); writes=%#v", got, cg.writes)
	}
	_ = res
}

// TestApplyDAGDiffRejectsPrimaryReclaimPartitionOverlap verifies fail-closed when
// widening the primary effective target would overlap a reclaim target. Masking
// the overlap by absorbing reclaimed cpus into the primary would silently break
// the partition invariant, so the apply must error instead.
func TestApplyDAGDiffRejectsPrimaryReclaimPartitionOverlap(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(1, 2)
	// live leaf sits on cpu 3, which belongs to the reclaim partition.
	cg.cpus["kubepods/burstable/pod-abc/container-a"] = machine.NewCPUSet(3)
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-abc"}
	cg.children["kubepods/burstable/pod-abc"] = []string{"container-a"}

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:                         dag,
		Cgroup:                      cg,
		ProtectUnmanagedKubePodLeaf: true,
		KubeManagedRelPrefix:        "kubepods",
	})
	if err == nil {
		t.Fatalf("expected partition overlap error, got nil; writes=%#v", cg.writes)
	}
	for _, want := range []string{"partition cpuset overlap", "kubepods", "reclaim", "overlap=3"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("partition overlap error missing %q; got %q", want, err.Error())
		}
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

// TestApplyDAGDiffWriteAndDescendStopsOnApplyFailure verifies that when
// expandDescendants fails to apply a cpuset at some intermediate rel, it does
// not continue writing further descendants under that failed parent. Otherwise
// grandchildren would be written against a parent target that never took
// effect, either violating the v1 parent-superset invariant or leaving them
// clamped below the real parent.
func TestApplyDAGDiffWriteAndDescendStopsOnApplyFailure(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"burstable"}
	cg.children["primary/burstable"] = []string{"leaf"}
	// The middle intermediate write fails; the grandchild must not be written.
	cg.failRel["primary/burstable"] = true

	_, err = ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
	})
	if err == nil {
		t.Fatalf("expected apply error from failed intermediate write, got nil; writes=%#v", cg.writes)
	}
	for _, w := range cg.writes {
		if w.rel == "primary/burstable/leaf" {
			t.Fatalf("grandchild must NOT be written after parent apply failure; writes=%#v", cg.writes)
		}
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
