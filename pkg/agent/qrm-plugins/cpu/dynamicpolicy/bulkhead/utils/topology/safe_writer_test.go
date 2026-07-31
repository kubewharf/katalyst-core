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
	"strings"
	"syscall"
	"testing"

	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
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

type dynamicRelEBUSYOnceCgroup struct {
	*topologyFakeCgroup

	rel      string
	attempts int
}

func (f *dynamicRelEBUSYOnceCgroup) ApplyCPUSet(ctx context.Context, rel string, data *cgcommon.CPUSetData) error {
	if rel == f.rel {
		f.attempts++
		if f.attempts == 1 {
			return syscall.EBUSY
		}
	}
	return f.topologyFakeCgroup.ApplyCPUSet(ctx, rel, data)
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

func TestFormatCPUSetSubtreeSnapshotEntryFiltersVersionSpecificFiles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		version     cgroupclient.CgroupVersion
		wantContain []string
		wantAbsent  []string
	}{
		{
			name:    "v1 prints sched load balance only",
			version: cgroupclient.CgroupVersionV1,
			wantContain: []string{
				`rel="kubepods"`,
				`cpus="0-3"`,
				`mems="0"`,
				`slb="0"`,
				`tasks="101,102"`,
				`procs="201"`,
			},
			wantAbsent: []string{
				"partition=",
			},
		},
		{
			name:    "v2 prints partition only",
			version: cgroupclient.CgroupVersionV2,
			wantContain: []string{
				`rel="kubepods"`,
				`cpus="0-3"`,
				`mems="0"`,
				`partition="root"`,
				`tasks="101,102"`,
				`procs="201"`,
			},
			wantAbsent: []string{
				"slb=",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cg := newTopologyFakeCgroup()
			cg.version = tt.version
			cg.files["kubepods"] = map[string][]byte{
				"cpuset.cpus":               []byte("0-3\n"),
				"cpuset.mems":               []byte("0\n"),
				"cpuset.sched_load_balance": []byte("0\n"),
				"cpuset.cpus.partition":     []byte("root\n"),
				"tasks":                     []byte("101\n102\n"),
				"cgroup.procs":              []byte("201\n"),
			}

			writer := newSafeCPUSetWriter(context.Background(), cg, "0", &DAGApplyResult{})
			got := writer.formatCPUSetSubtreeSnapshotEntry("kubepods")
			for _, want := range tt.wantContain {
				if !strings.Contains(got, want) {
					t.Fatalf("entry %q does not contain %q", got, want)
				}
			}
			for _, absent := range tt.wantAbsent {
				if strings.Contains(got, absent) {
					t.Fatalf("entry %q contains unexpected %q", got, absent)
				}
			}
		})
	}
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
	retryObservations := maxSafeCPUSetWriteAttempts - 1
	diagnosticSubtreeLists := maxSafeCPUSetWriteAttempts
	if wantLists := retryObservations + diagnosticSubtreeLists; cg.childLists != wantLists || cg.childReads != retryObservations {
		t.Fatalf("fresh child observations after retryable EBUSY = lists:%d reads:%d, want lists:%d reads:%d",
			cg.childLists, cg.childReads, wantLists, retryObservations)
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
	wantErr := errors.New("permission denied")
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

func TestSafeCPUSetWriterRetriesDynamicRelAfterEBUSYWithChildShrink(t *testing.T) {
	t.Parallel()

	base := newTopologyFakeCgroup()
	base.cpus["kubepods/pod0"] = machine.NewCPUSet(0, 1, 2)
	base.cpus["kubepods/pod0/container0"] = machine.NewCPUSet(0, 2)
	base.children["kubepods/pod0"] = []string{"container0"}
	cg := &dynamicRelEBUSYOnceCgroup{
		topologyFakeCgroup: base,
		rel:                "kubepods/pod0",
	}

	writer := newSafeCPUSetWriter(context.Background(), cg, "0", &DAGApplyResult{})
	if err := writer.writeDynamicRel("kubepods/pod0", machine.NewCPUSet(0, 1), ""); err != nil {
		t.Fatalf("writeDynamicRel error = %v, want retry to converge child then parent; writes=%#v", err, cg.writes)
	}
	if cg.attempts != 2 {
		t.Fatalf("dynamic rel attempts = %d, want 2", cg.attempts)
	}
	wantWrites := []cpusetWrite{
		{rel: "kubepods/pod0/container0", cpus: "0", mems: ""},
		{rel: "kubepods/pod0", cpus: "0-1", mems: ""},
	}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes=%#v, want child shrink before parent retry %#v", cg.writes, wantWrites)
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

func TestSafeCPUSetWriterNormalizesEmptyReclaimBucketChildWithMems(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75), Mems: "0-1"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(29, 30, 31, 73, 74, 75), Mems: "1", Metadata: map[string]string{"numa": "1"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	bucket := dag.index["kubesandbox/reclaimed-1"]
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox"] = machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75)
	cg.cpus[bucket.Rel] = machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1/sandbox022"] = machine.NewCPUSet(5, 6, 7, 53, 54, 55)
	cg.children["kubesandbox"] = []string{"reclaimed-1"}
	cg.children[bucket.Rel] = []string{"sandbox022"}
	res := DAGApplyResult{}

	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &res)
	if err := writer.shrinkParentWithLiveChildUnion(bucket, machine.NewCPUSet(29, 30, 31, 73, 74, 75)); err != nil {
		t.Fatalf("shrinkParentWithLiveChildUnion: %v writes=%#v", err, cg.writes)
	}

	wantWrites := []cpusetWrite{
		{rel: "kubesandbox/reclaimed-1/sandbox022", cpus: "29-31,73-75", mems: "1"},
		{rel: "kubesandbox/reclaimed-1", cpus: "29-31,73-75", mems: "1"},
	}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, wantWrites)
	}
	if got, want := cg.cpus["kubesandbox/reclaimed-1/sandbox022"], machine.NewCPUSet(29, 30, 31, 73, 74, 75); !got.Equals(want) {
		t.Fatalf("sandbox cpuset = %s, want %s", got.String(), want.String())
	}
}

// TestSafeCPUSetWriterGrowsParentBeforeReclaimBucketBridgeExpansion reproduces
// the converge_shrink drain scenario where a reclaim NUMA bucket must be
// bridged to a net-expanded target (it gains CPUs entering from a sibling NUMA
// node) while the real parent (kubesandbox) does not yet contain those CPUs.
// cgroup v1 rejects writing a child cpuset outside its parent, so the parent
// must be grown to a superset of the bridge target first. Before the fix the
// reclaim-bucket bridge path skipped that parent grow and the write failed with
// EACCES ("permission denied") on kubesandbox/reclaimed-0/cpuset.cpus.
func TestSafeCPUSetWriterGrowsParentBeforeReclaimBucketBridgeExpansion(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(5, 6, 7, 50, 51, 52, 53, 54, 55), Mems: "0-1"},
		{Rel: "kubesandbox/reclaimed-0", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(3, 4, 5, 6, 7, 8, 9, 50, 51, 52, 53, 54, 55, 56, 57), Mems: "0", Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	bucket := dag.index["kubesandbox/reclaimed-0"]
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	// Parent lacks 3,4,8,9,56,57 that the bucket bridge target introduces.
	cg.cpus["kubesandbox"] = machine.NewCPUSet(5, 6, 7, 50, 51, 52, 53, 54, 55)
	cg.cpus[bucket.Rel] = machine.NewCPUSet(5, 6, 7, 50, 51, 52, 53, 54, 55)
	cg.cpus["kubesandbox/reclaimed-0/sandbox022"] = machine.NewCPUSet(5, 6, 7, 50, 51, 52, 53, 54, 55)
	cg.children["kubesandbox"] = []string{"reclaimed-0"}
	cg.children[bucket.Rel] = []string{"sandbox022"}
	res := DAGApplyResult{}

	target := machine.NewCPUSet(3, 4, 5, 6, 7, 8, 9, 50, 51, 52, 53, 54, 55, 56, 57)
	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &res)
	if err := writer.shrinkParentWithLiveChildUnion(bucket, target); err != nil {
		t.Fatalf("shrinkParentWithLiveChildUnion: %v writes=%#v", err, cg.writes)
	}

	// The parent must have been grown to a superset of the bridge target before
	// the reclaim-bucket bridge write.
	parentGrow, bucketBridge := -1, -1
	for i, write := range cg.writes {
		if write.rel == "kubesandbox" {
			cpus := machine.MustParse(write.cpus)
			if target.IsSubsetOf(cpus) && parentGrow < 0 {
				parentGrow = i
			}
		}
		if write.rel == bucket.Rel && write.cpus == "3-9,50-57" && bucketBridge < 0 {
			bucketBridge = i
		}
	}
	if parentGrow < 0 {
		t.Fatalf("parent kubesandbox was not grown to superset of bridge target; writes=%#v", cg.writes)
	}
	if bucketBridge < 0 {
		t.Fatalf("reclaim bucket bridge write to 3-9,50-57 not observed; writes=%#v", cg.writes)
	}
	if parentGrow >= bucketBridge {
		t.Fatalf("parent grow (idx %d) must precede reclaim bucket bridge write (idx %d); writes=%#v",
			parentGrow, bucketBridge, cg.writes)
	}
	if got := cg.cpus[bucket.Rel]; !got.Equals(target) {
		t.Fatalf("reclaim bucket cpuset = %s, want %s; writes=%#v", got.String(), target.String(), cg.writes)
	}
	if got := cg.cpus["kubesandbox"]; !target.IsSubsetOf(got) {
		t.Fatalf("parent kubesandbox cpuset = %s, want superset of %s; writes=%#v", got.String(), target.String(), cg.writes)
	}
}

// TestSafeCPUSetWriterGrowsIntermediateParentBeforeReclaimDescendantLeafExpansion
// reproduces the production NUMA drain where a reclaim NUMA bucket shifts to a
// new NUMA range and a two-level dynamic descendant (per-container sandbox ->
// kata leaf) must move with it. normalizeReclaimBucketDescendants recurses
// post-order (leaf first), so before the fix it wrote the leaf into the new
// range 33-39,81-87 while the intermediate sandbox parent still held the old
// range 29-31,73-79. cgroup v1 rejects a child cpuset outside its parent, so the
// leaf apply failed with EACCES ("permission denied") and blocked the advisor
// loop. The fix grows every uncontrolled intermediate parent to a superset of
// the child target before the leaf write.
func TestSafeCPUSetWriterGrowsIntermediateParentBeforeReclaimDescendantLeafExpansion(t *testing.T) {
	t.Parallel()

	oldRange := machine.NewCPUSet(29, 30, 31, 73, 74, 75)
	newRange := machine.NewCPUSet(33, 34, 35, 36, 37, 38, 39, 81, 82, 83, 84, 85, 86, 87)
	// The bucket bridge during the drain must cover both the outgoing and the
	// incoming NUMA range so live descendants can move.
	bucketBridge := oldRange.Union(newRange)

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: bucketBridge, Mems: "0-1"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: newRange, Mems: "1", Metadata: map[string]string{"numa": "1"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	bucket := dag.index["kubesandbox/reclaimed-1"]
	sandboxRel := "kubesandbox/reclaimed-1/sandbox9b"
	leafRel := "kubesandbox/reclaimed-1/sandbox9b/kata54"

	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	// Everything currently sits on the old NUMA range.
	cg.cpus["kubesandbox"] = bucketBridge
	cg.cpus[bucket.Rel] = oldRange
	cg.cpus[sandboxRel] = oldRange
	cg.cpus[leafRel] = oldRange
	cg.children["kubesandbox"] = []string{"reclaimed-1"}
	cg.children[bucket.Rel] = []string{"sandbox9b"}
	cg.children[sandboxRel] = []string{"kata54"}
	res := DAGApplyResult{}

	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &res)
	if err := writer.shrinkParentWithLiveChildUnion(bucket, newRange); err != nil {
		t.Fatalf("shrinkParentWithLiveChildUnion: %v writes=%#v", err, cg.writes)
	}

	// The intermediate sandbox parent must be grown to a superset of the leaf
	// target strictly before the leaf itself is written into the new range.
	sandboxGrow, leafWrite := -1, -1
	for i, write := range cg.writes {
		if write.rel == sandboxRel {
			cpus := machine.MustParse(write.cpus)
			if newRange.IsSubsetOf(cpus) && sandboxGrow < 0 {
				sandboxGrow = i
			}
		}
		if write.rel == leafRel && machine.MustParse(write.cpus).Equals(newRange) && leafWrite < 0 {
			leafWrite = i
		}
	}
	if sandboxGrow < 0 {
		t.Fatalf("intermediate sandbox parent was not grown to superset of leaf target; writes=%#v", cg.writes)
	}
	if leafWrite < 0 {
		t.Fatalf("leaf write to new NUMA range not observed; writes=%#v", cg.writes)
	}
	if sandboxGrow >= leafWrite {
		t.Fatalf("sandbox grow (idx %d) must precede leaf write (idx %d); writes=%#v",
			sandboxGrow, leafWrite, cg.writes)
	}
	if got := cg.cpus[leafRel]; !got.Equals(newRange) {
		t.Fatalf("leaf cpuset = %s, want %s; writes=%#v", got.String(), newRange.String(), cg.writes)
	}
	if got := cg.cpus[sandboxRel]; !got.Equals(newRange) {
		t.Fatalf("sandbox cpuset = %s, want %s; writes=%#v", got.String(), newRange.String(), cg.writes)
	}
	if got := cg.cpus[bucket.Rel]; !got.Equals(newRange) {
		t.Fatalf("reclaim bucket cpuset = %s, want %s; writes=%#v", got.String(), newRange.String(), cg.writes)
	}
}

func TestDynamicDescendantPolicyClampsLiveReclaimChildToBucketTarget(t *testing.T) {
	t.Parallel()

	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(29, 30, 31, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1/sandbox022"] = machine.NewCPUSet(0, 1, 2, 3, 4, 5)
	cg.files["kubesandbox/reclaimed-1/sandbox022"] = map[string][]byte{
		"tasks":        []byte("123\n"),
		"cgroup.procs": []byte("123\n"),
	}

	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &DAGApplyResult{})
	writer.controlledByRel["kubesandbox/reclaimed-1"] = &TopoNode{
		Rel:      "kubesandbox/reclaimed-1",
		Role:     TopoNodeRoleReclaimNUMABucket,
		CPUs:     machine.NewCPUSet(29, 30, 31, 73, 74, 75),
		Mems:     "1",
		Metadata: map[string]string{"numa": "1"},
	}
	writer.targetByRel["kubesandbox/reclaimed-1"] = machine.NewCPUSet(29, 30, 31, 73, 74, 75)

	target, mems, guarded := writer.dynamicPolicy.Resolve(
		"kubesandbox/reclaimed-1/sandbox022",
		machine.NewCPUSet(5, 6, 7, 53, 54, 55),
	)

	if !guarded {
		t.Fatalf("guarded = false, want true")
	}
	if !target.Equals(machine.NewCPUSet(29, 30, 31, 73, 74, 75)) {
		t.Fatalf("target = %s, want 29-31,73-75", target.String())
	}
	if mems != "1" {
		t.Fatalf("mems = %q, want 1", mems)
	}
}

func TestSafeCPUSetWriterClampsLiveReclaimBucketChildOutsideNUMA(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75), Mems: "0-1"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(29, 30, 31, 73, 74, 75), Mems: "1", Metadata: map[string]string{"numa": "1"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	bucket := dag.index["kubesandbox/reclaimed-1"]
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox"] = machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75)
	cg.cpus[bucket.Rel] = machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1/sandbox022"] = machine.NewCPUSet(5, 6, 7, 53, 54, 55)
	cg.children["kubesandbox"] = []string{"reclaimed-1"}
	cg.children[bucket.Rel] = []string{"sandbox022"}
	cg.files["kubesandbox/reclaimed-1/sandbox022"] = map[string][]byte{
		"tasks":        []byte("123\n"),
		"cgroup.procs": []byte("123\n"),
	}
	res := DAGApplyResult{}

	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &res)
	if err := writer.shrinkParentWithLiveChildUnion(bucket, machine.NewCPUSet(29, 30, 31, 73, 74, 75)); err != nil {
		t.Fatalf("shrinkParentWithLiveChildUnion: %v writes=%#v", err, cg.writes)
	}
	wantWrites := []cpusetWrite{
		{rel: "kubesandbox/reclaimed-1/sandbox022", cpus: "29-31,73-75", mems: "1"},
		{rel: "kubesandbox/reclaimed-1", cpus: "29-31,73-75", mems: "1"},
	}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, wantWrites)
	}
}

func TestSafeCPUSetWriterWriteDynamicRelClampsReclaimDescendant(t *testing.T) {
	t.Parallel()

	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox"] = machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(29, 30, 31, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1/sandbox022"] = machine.NewCPUSet(5, 6, 7, 53, 54, 55)
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &res)
	writer.controlledByRel["kubesandbox/reclaimed-1"] = &TopoNode{
		Rel:      "kubesandbox/reclaimed-1",
		Role:     TopoNodeRoleReclaimNUMABucket,
		CPUs:     machine.NewCPUSet(29, 30, 31, 73, 74, 75),
		Mems:     "1",
		Metadata: map[string]string{"numa": "1"},
	}
	writer.targetByRel["kubesandbox/reclaimed-1"] = machine.NewCPUSet(29, 30, 31, 73, 74, 75)

	if err := writer.writeDynamicRel("kubesandbox/reclaimed-1/sandbox022", machine.NewCPUSet(5, 6, 7, 53, 54, 55), ""); err != nil {
		t.Fatalf("writeDynamicRel: %v", err)
	}
	want := []cpusetWrite{{rel: "kubesandbox/reclaimed-1/sandbox022", cpus: "29-31,73-75", mems: "1"}}
	if !reflect.DeepEqual(cg.writes, want) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, want)
	}
}

func TestSafeCPUSetWriterWriteDynamicRelFallsBackToBucketCurrentWhenTargetDisjoint(t *testing.T) {
	t.Parallel()

	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(29, 30, 31, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1/sandbox022"] = machine.NewCPUSet(0, 1, 2, 3, 4, 5)

	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &DAGApplyResult{})
	writer.controlledByRel["kubesandbox/reclaimed-1"] = &TopoNode{
		Rel:      "kubesandbox/reclaimed-1",
		Role:     TopoNodeRoleReclaimNUMABucket,
		CPUs:     machine.NewCPUSet(29, 30, 31, 73, 74, 75),
		Mems:     "1",
		Metadata: map[string]string{"numa": "1"},
	}
	writer.targetByRel["kubesandbox/reclaimed-1"] = machine.NewCPUSet(5, 6, 7, 53, 54, 55)

	if err := writer.writeDynamicRel("kubesandbox/reclaimed-1/sandbox022", machine.NewCPUSet(5, 6, 7, 53, 54, 55), ""); err != nil {
		t.Fatalf("writeDynamicRel: %v", err)
	}
	want := []cpusetWrite{{rel: "kubesandbox/reclaimed-1/sandbox022", cpus: "29-31,73-75", mems: "1"}}
	if !reflect.DeepEqual(cg.writes, want) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, want)
	}
}

func TestSafeCPUSetWriterWriteDynamicRelUsesPhysicalReclaimBucketWhenDAGMissesBucket(t *testing.T) {
	t.Parallel()

	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(29, 30, 31, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1/sandbox022"] = machine.NewCPUSet(0, 1, 2, 3, 4, 5)

	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &DAGApplyResult{})

	if err := writer.writeDynamicRel("kubesandbox/reclaimed-1/sandbox022", machine.NewCPUSet(5, 6, 7, 53, 54, 55), ""); err != nil {
		t.Fatalf("writeDynamicRel: %v", err)
	}
	want := []cpusetWrite{{rel: "kubesandbox/reclaimed-1/sandbox022", cpus: "29-31,73-75", mems: "1"}}
	if !reflect.DeepEqual(cg.writes, want) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, want)
	}
}

// TestSafeCPUSetWriterParksUncontrolledPhysicalReclaimBucketCrossNUMA verifies
// the NUMA-drain transient where the parent (kubesandbox) is targeted to a
// single NUMA node while an uncontrolled physical reclaim bucket from the other
// NUMA node still owns CPUs. Clamping that bucket to the parent target would
// inject foreign-NUMA CPUs, so it must be parked (left untouched) rather than
// corrupted. Parking must NOT fail the apply (a hard error fails Pod admission
// with UnexpectedAdmissionError); instead the parent retains the parked CPUs as
// a temporary cgroup v1 superset until a later advisor round drains the bucket
// through its own controlled transition. Reproduces the production failure
// parent=kubesandbox child=kubesandbox/reclaimed-0 numa=0 (here reclaimed-1 on
// numa=1 is the parked cross-NUMA bucket).
func TestSafeCPUSetWriterParksUncontrolledPhysicalReclaimBucketCrossNUMA(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(5, 6, 7, 53, 54, 55), Mems: "0-1"},
		{Rel: "kubesandbox/reclaimed-0", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(5, 6, 7, 53, 54, 55), Mems: "0", Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox"] = machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-0"] = machine.NewCPUSet(5, 6, 7, 53, 54, 55)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(29, 30, 31, 73, 74, 75)
	cg.children["kubesandbox"] = []string{"reclaimed-0", "reclaimed-1"}

	cpuDetails := machine.CPUDetails{}
	for _, cpu := range []int{5, 6, 7, 53, 54, 55} {
		cpuDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: 0}
	}
	for _, cpu := range []int{29, 30, 31, 73, 74, 75} {
		cpuDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: 1}
	}

	if _, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:        dag,
		Cgroup:     cg,
		Mems:       "0-1",
		CPUDetails: cpuDetails,
	}); err != nil {
		t.Fatalf("ApplyDAGDiff should park the cross-NUMA bucket, not fail; err=%v writes=%#v", err, cg.writes)
	}
	for _, write := range cg.writes {
		if write.rel == "kubesandbox/reclaimed-1" {
			t.Fatalf("uncontrolled cross-NUMA reclaim bucket must not be written; writes=%#v", cg.writes)
		}
	}
	if got, want := cg.cpus["kubesandbox/reclaimed-1"], machine.NewCPUSet(29, 30, 31, 73, 74, 75); !got.Equals(want) {
		t.Fatalf("reclaimed-1 cpuset = %s, want unchanged %s; writes=%#v", got.String(), want.String(), cg.writes)
	}
	// The parent must retain the parked NUMA1 CPUs as a superset; it cannot be
	// shrunk below target(NUMA0) ∪ parked(NUMA1) while the bucket still lives.
	if got := cg.cpus["kubesandbox"]; !got.Contains(29) || !got.Contains(73) {
		t.Fatalf("parent kubesandbox = %s, must retain parked NUMA1 CPUs; writes=%#v", got.String(), cg.writes)
	}
}

func TestSafeCPUSetWriterShrinkDynamicRelClampsReclaimDescendant(t *testing.T) {
	t.Parallel()

	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox"] = machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(29, 30, 31, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1/sandbox022"] = machine.NewCPUSet(5, 6, 7, 53, 54, 55)
	res := DAGApplyResult{}
	writer := newSafeCPUSetWriter(context.Background(), cg, "0-1", &res)
	writer.controlledByRel["kubesandbox/reclaimed-1"] = &TopoNode{
		Rel:      "kubesandbox/reclaimed-1",
		Role:     TopoNodeRoleReclaimNUMABucket,
		CPUs:     machine.NewCPUSet(29, 30, 31, 73, 74, 75),
		Mems:     "1",
		Metadata: map[string]string{"numa": "1"},
	}
	writer.targetByRel["kubesandbox/reclaimed-1"] = machine.NewCPUSet(29, 30, 31, 73, 74, 75)

	if _, err := writer.shrinkDynamicRelToParent("kubesandbox/reclaimed-1/sandbox022", machine.NewCPUSet(5, 6, 7, 53, 54, 55), 0); err != nil {
		t.Fatalf("shrinkDynamicRelToParent: %v", err)
	}
	want := []cpusetWrite{{rel: "kubesandbox/reclaimed-1/sandbox022", cpus: "29-31,73-75", mems: "1"}}
	if !reflect.DeepEqual(cg.writes, want) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, want)
	}
}

func TestApplyCPUSetWritesRequestedTargetWithoutTopologyPolicy(t *testing.T) {
	t.Parallel()

	cg := newTopologyFakeCgroup()
	cg.cpus["kubesandbox/reclaimed-1/sandbox022"] = machine.NewCPUSet(29, 30, 31)

	if err := applyCPUSet(context.Background(), cg, "kubesandbox/reclaimed-1/sandbox022", machine.NewCPUSet(5, 6, 7, 53, 54, 55), ""); err != nil {
		t.Fatalf("applyCPUSet: %v", err)
	}
	want := []cpusetWrite{{rel: "kubesandbox/reclaimed-1/sandbox022", cpus: "5-7,53-55", mems: ""}}
	if !reflect.DeepEqual(cg.writes, want) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, want)
	}
}

func TestSafeCPUSetWriterDoesNotPenetrateControlledChildren(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubesandbox", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75), Mems: "0-1"},
		{Rel: "kubesandbox/reclaimed-1", ParentRel: "kubesandbox", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(29, 30, 31, 73, 74, 75), Mems: "1", Metadata: map[string]string{"numa": "1"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubesandbox"] = machine.NewCPUSet(5, 6, 7, 29, 30, 31, 53, 54, 55, 73, 74, 75, 80)
	cg.cpus["kubesandbox/reclaimed-1"] = machine.NewCPUSet(29, 30, 31, 73, 74, 75)
	cg.cpus["kubesandbox/reclaimed-1/sandbox022"] = machine.NewCPUSet(5, 6, 7, 53, 54, 55)
	cg.children["kubesandbox"] = []string{"reclaimed-1"}
	cg.children["kubesandbox/reclaimed-1"] = []string{"sandbox022"}
	cpuDetails := machine.CPUDetails{}
	for _, cpu := range []int{5, 6, 7, 53, 54, 55, 80} {
		cpuDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: 0}
	}
	for _, cpu := range []int{29, 30, 31, 73, 74, 75} {
		cpuDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: 1}
	}

	if _, err := ApplyDAGDiff(context.Background(), DAGApplyInputs{
		DAG:        dag,
		Cgroup:     cg,
		Mems:       "0-1",
		CPUDetails: cpuDetails,
	}); err != nil {
		t.Fatalf("ApplyDAGDiff: %v writes=%#v", err, cg.writes)
	}

	for _, write := range cg.writes {
		if strings.HasPrefix(write.rel, "kubesandbox/reclaimed-1/sandbox022") {
			t.Fatalf("parent-level shrink should not penetrate controlled child descendants, writes=%#v", cg.writes)
		}
	}
}
