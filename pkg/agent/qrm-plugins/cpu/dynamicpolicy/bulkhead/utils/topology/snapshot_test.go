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
	"reflect"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type stableIdentityTestDriver struct {
	HierarchyDriver
}

func (d *stableIdentityTestDriver) Capabilities() HierarchyCapabilities {
	capabilities := d.HierarchyDriver.Capabilities()
	capabilities.StableIdentity = true
	return capabilities
}

func TestSnapshotIncludesControlledRootsBucketsAndDynamicDescendants(t *testing.T) {
	t.Parallel()
	dag := buildSnapshotTestDAG(t)
	fake := buildSnapshotTestHierarchy()

	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, dag, SnapshotRequest{
		Purpose:      ScanForPlan,
		AffectedRels: []string{"primary", "reclaim", "reclaim/bucket-0"},
	}, NewBudgetTracker(ConvergenceBudget{}))
	if err != nil {
		t.Fatalf("BuildCompleteSnapshot() error = %v", err)
	}

	wantRels := []string{
		"primary", "primary/pod-a", "primary/pod-a/container-a",
		"reclaim", "reclaim/bucket-0", "reclaim/bucket-0/pod-r",
	}
	for _, rel := range wantRels {
		if _, ok := snapshot.Entries[rel]; !ok {
			t.Errorf("Entries missing %q", rel)
		}
	}
	if got := snapshot.DomainUnion[DomainPrimary].String(); got != "0-1" {
		t.Fatalf("primary union = %q, want 0-1", got)
	}
	if got := snapshot.DomainUnion[DomainReclaim].String(); got != "2-3" {
		t.Fatalf("reclaim union = %q, want 2-3", got)
	}
	if snapshot.ID == (SnapshotID{}) {
		t.Fatal("snapshot ID is zero")
	}
}

func TestSnapshotCollectsActivityOnlyForReclaimNUMABucketCandidates(t *testing.T) {
	t.Parallel()
	dag := buildSnapshotTestDAG(t)
	fake := newFakeHierarchyDriver()
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")
	fake.add("primary/holder/leaf", CgroupIdentity{Device: 1, Inode: 2}, "1", "0")
	fake.add("reclaim", CgroupIdentity{Device: 1, Inode: 3}, "2-3", "0")
	fake.add("reclaim/bucket-0", CgroupIdentity{Device: 1, Inode: 4}, "2-3", "0")
	fake.add("reclaim/bucket-0/holder", CgroupIdentity{Device: 1, Inode: 5}, "3", "0")
	fake.nodes["reclaim/bucket-0/holder"].activity = CgroupActivity{TasksEmpty: true}

	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, dag, SnapshotRequest{
		Purpose:                ScanForPlan,
		AffectedRels:           []string{"primary", "reclaim", "reclaim/bucket-0"},
		CollectDormantActivity: true,
	}, NewBudgetTracker(ConvergenceBudget{}))
	if err != nil {
		t.Fatalf("BuildCompleteSnapshot() error = %v", err)
	}
	if got := snapshot.Entries["reclaim/bucket-0"].Activity; got.Inactive() || got.Childless {
		t.Fatalf("bucket activity = %+v, want non-leaf proof", got)
	}
	if got := snapshot.Entries["reclaim/bucket-0/holder"].Activity; got != (CgroupActivity{}) {
		t.Fatalf("dynamic descendant activity = %+v, want no activity scan below non-leaf bucket", got)
	}
	if got := snapshot.Entries["primary"].Activity; got != (CgroupActivity{}) {
		t.Fatalf("primary activity = %+v, want no unnecessary membership scan", got)
	}
}

func TestSnapshotCollectsActivityForBucketThatBecomesEmptyAfterNormalization(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0), TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true},
		{
			Rel: "reclaim/bucket-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket,
			Domain: DomainReclaim, CPUs: machine.NewCPUSet(0),
			Constraint: TopologyConstraint{CPUUpperBound: machine.NewCPUSet(0), Scope: TopologyScopeNUMANode},
			Metadata:   map[string]string{"numa": "0"},
		},
	})
	fake := newFakeHierarchyDriver()
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
	fake.add("reclaim", CgroupIdentity{Device: 1, Inode: 2}, "1", "0")
	fake.add("reclaim/bucket-0", CgroupIdentity{Device: 1, Inode: 3}, "0-1", "0")
	fake.markInactive("reclaim/bucket-0")

	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, dag, SnapshotRequest{
		Purpose:                ScanForPlan,
		AffectedRels:           []string{"primary", "reclaim", "reclaim/bucket-0"},
		CollectDormantActivity: true,
	}, NewBudgetTracker(ConvergenceBudget{}))
	if err != nil {
		t.Fatalf("BuildCompleteSnapshot() error = %v", err)
	}
	if got := snapshot.Entries["reclaim/bucket-0"].Activity; !got.Inactive() {
		t.Fatalf("bucket activity = %+v, want proof collected before effective-target normalization", got)
	}

	effective, err := computeEffectiveTargets(
		dag, false,
		machine.CPUDetails{0: {NUMANodeID: 0}, 1: {NUMANodeID: 1}},
		nil, nil,
	)
	if err != nil {
		t.Fatalf("computeEffectiveTargets() error = %v", err)
	}
	if !effective["reclaim/bucket-0"].IsEmpty() {
		t.Fatalf("normalized bucket target = %s, want empty", effective["reclaim/bucket-0"].String())
	}
	materialized := materializeTargets(dag, snapshot, false, effective)
	if _, dormant := materialized.DormantRels["reclaim/bucket-0"]; !dormant {
		t.Fatal("bucket that became empty after normalization was not classified dormant")
	}
}

func TestSnapshotFailsClosedForDormantLeafActivityAndChildRaces(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(1), TrustAnchor: true},
		{Rel: "reclaim/bucket-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet()},
	})

	t.Run("task migrates into scanned node", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.add("reclaim", CgroupIdentity{Device: 1, Inode: 1}, "1", "0")
		fake.add("reclaim/bucket-0", CgroupIdentity{Device: 1, Inode: 2}, "0-1", "0")
		fake.beforeCall = func(op HierarchyOperation, rel string) error {
			if op == HierarchyOperationRead && rel == "reclaim/bucket-0" {
				fake.nodes[rel].activity.TasksEmpty = false
			}
			return nil
		}

		snapshot, err := BuildCompleteSnapshot(context.Background(), fake, dag, SnapshotRequest{
			Purpose:                ScanForPlan,
			AffectedRels:           []string{"reclaim/bucket-0"},
			CollectDormantActivity: true,
		}, NewBudgetTracker(ConvergenceBudget{}))
		if err != nil {
			t.Fatalf("BuildCompleteSnapshot() error = %v", err)
		}
		if got := materializeTargets(dag, snapshot, false, desiredTargets(dag)); len(got.DormantRels) != 0 {
			t.Fatalf("busy bucket classified dormant: %v", got.DormantRels)
		}
	})

	t.Run("new child appears during atomic activity read", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.add("reclaim", CgroupIdentity{Device: 1, Inode: 1}, "1", "0")
		fake.add("reclaim/bucket-0", CgroupIdentity{Device: 1, Inode: 2}, "0-1", "0")
		fake.beforeCall = func(op HierarchyOperation, rel string) error {
			if op == HierarchyOperationRead && rel == "reclaim/bucket-0" {
				fake.add("reclaim/bucket-0/new", CgroupIdentity{Device: 1, Inode: 3}, "0", "0")
			}
			return nil
		}

		snapshot, err := BuildCompleteSnapshot(context.Background(), fake, dag, SnapshotRequest{
			Purpose:                ScanForPlan,
			AffectedRels:           []string{"reclaim/bucket-0"},
			CollectDormantActivity: true,
		}, NewBudgetTracker(ConvergenceBudget{}))
		if err == nil || snapshot != nil {
			t.Fatalf("snapshot=%v error=%v, want fail-closed child creation", snapshot, err)
		}
	})

	t.Run("child identity changes during atomic activity read", func(t *testing.T) {
		t.Parallel()
		fake := newFakeHierarchyDriver()
		fake.add("reclaim", CgroupIdentity{Device: 1, Inode: 1}, "1", "0")
		fake.add("reclaim/bucket-0", CgroupIdentity{Device: 1, Inode: 2}, "0-1", "0")
		fake.add("reclaim/bucket-0/child", CgroupIdentity{Device: 1, Inode: 3}, "0", "0")
		fake.beforeCall = func(op HierarchyOperation, rel string) error {
			if op == HierarchyOperationRead && rel == "reclaim/bucket-0" {
				fake.bumpIdentity("reclaim/bucket-0/child")
			}
			return nil
		}

		snapshot, err := BuildCompleteSnapshot(context.Background(), fake, dag, SnapshotRequest{
			Purpose:                ScanForPlan,
			AffectedRels:           []string{"reclaim/bucket-0"},
			CollectDormantActivity: true,
		}, NewBudgetTracker(ConvergenceBudget{}))
		if err == nil || snapshot != nil {
			t.Fatalf("snapshot=%v error=%v, want fail-closed child ABA rejection", snapshot, err)
		}
	})
}

func TestSnapshotRejectsDriverWithoutStableIdentityBeforeIO(t *testing.T) {
	t.Parallel()
	fake := buildSnapshotTestHierarchy()
	fake.stableIdentity = false

	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
		Purpose:      ScanForPlan,
		AffectedRels: []string{"primary"},
	}, NewBudgetTracker(ConvergenceBudget{}))
	if snapshot != nil {
		t.Fatalf("snapshot = %#v, want nil", snapshot)
	}
	var snapshotErr *SnapshotError
	if !errors.As(err, &snapshotErr) {
		t.Fatalf("error = %T %v, want *SnapshotError", err, err)
	}
	if snapshotErr.Class != HierarchyErrorInvalid {
		t.Fatalf("error class = %q, want %q", snapshotErr.Class, HierarchyErrorInvalid)
	}
	if fake.calls != 0 {
		t.Fatalf("driver calls = %d, want 0", fake.calls)
	}
}

func TestSnapshotRejectsReadCPUSetReadMemsAndListChildrenFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		op   HierarchyOperation
		rel  string
		err  error
	}{
		{name: "cpuset read", op: HierarchyOperationRead, rel: "primary", err: errors.New("parse cpuset: invalid syntax")},
		{name: "mems read", op: HierarchyOperationRead, rel: "reclaim", err: errors.New("read cpuset.mems: permission denied")},
		{name: "children list", op: HierarchyOperationList, rel: "primary", err: errors.New("readdir: input/output error")},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := buildSnapshotTestHierarchy()
			fake.beforeCall = func(op HierarchyOperation, rel string) error {
				if op == tt.op && rel == tt.rel {
					return tt.err
				}
				return nil
			}
			snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
				Purpose:      ScanForPlan,
				AffectedRels: []string{"primary", "reclaim"},
			}, NewBudgetTracker(ConvergenceBudget{}))
			if err == nil || snapshot != nil {
				t.Fatalf("snapshot=%v error=%v, want nil snapshot and error", snapshot, err)
			}
			var snapshotErr *SnapshotError
			if !errors.As(err, &snapshotErr) {
				t.Fatalf("error type = %T, want *SnapshotError", err)
			}
		})
	}
}

func TestSnapshotSkipsUncontrolledCgroupV2DescendantWithoutCpusetController(t *testing.T) {
	t.Parallel()
	fake := buildSnapshotTestHierarchy()
	fake.capabilities.EffectiveCPUSet = true
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "primary/pod-a/container-a" {
			return ErrCgroupControllerUnavailable
		}
		return nil
	}

	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
		Purpose:      ScanForPlan,
		AffectedRels: []string{"primary"},
	}, NewBudgetTracker(ConvergenceBudget{}))
	if err != nil {
		t.Fatalf("BuildCompleteSnapshot() error = %v", err)
	}
	if _, ok := snapshot.Entries["primary"]; !ok {
		t.Fatalf("snapshot missing controlled root")
	}
	if _, ok := snapshot.Entries["primary/pod-a"]; !ok {
		t.Fatalf("snapshot missing dynamic parent with cpuset controller")
	}
	if _, ok := snapshot.Entries["primary/pod-a/container-a"]; ok {
		t.Fatalf("snapshot included uncontrolled descendant without cpuset controller")
	}
	childIdentity := fake.nodes["primary/pod-a/container-a"].identity
	wantEvidence := UnavailableChildEvidence{
		Identity: childIdentity,
		Reason:   UnavailableChildReasonControllerUnavailable,
	}
	if got, ok := snapshot.UnavailableChildren["primary/pod-a/container-a"]; !ok || got != wantEvidence {
		t.Fatalf("unavailable-child evidence = %v, want %v", snapshot.UnavailableChildren, wantEvidence)
	}
	if got := snapshot.DomainUnion[DomainPrimary].String(); got != "0-1" {
		t.Fatalf("primary domain union = %q, want 0-1", got)
	}
}

func TestSnapshotRejectsControlledCgroupV2NodeWithoutCpusetController(t *testing.T) {
	t.Parallel()
	fake := buildSnapshotTestHierarchy()
	fake.capabilities.EffectiveCPUSet = true
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "reclaim/bucket-0" {
			return ErrCgroupControllerUnavailable
		}
		return nil
	}

	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
		Purpose:      ScanForPlan,
		AffectedRels: []string{"reclaim/bucket-0"},
	}, NewBudgetTracker(ConvergenceBudget{}))
	if snapshot != nil {
		t.Fatalf("snapshot = %#v, want nil", snapshot)
	}
	if !errors.Is(err, ErrCgroupControllerUnavailable) {
		t.Fatalf("error = %v, want ErrCgroupControllerUnavailable", err)
	}
}

func TestSnapshotDoesNotSkipUnavailableControllerOnCgroupV1(t *testing.T) {
	t.Parallel()
	fake := buildSnapshotTestHierarchy()
	fake.capabilities.EffectiveCPUSet = false
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "primary/pod-a/container-a" {
			return ErrCgroupControllerUnavailable
		}
		return nil
	}

	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
		Purpose:      ScanForPlan,
		AffectedRels: []string{"primary"},
	}, NewBudgetTracker(ConvergenceBudget{}))
	if snapshot != nil {
		t.Fatalf("snapshot = %#v, want nil", snapshot)
	}
	if !errors.Is(err, ErrCgroupControllerUnavailable) {
		t.Fatalf("error = %v, want ErrCgroupControllerUnavailable", err)
	}
}

func TestSnapshotRejectsIdentityChangeAndListStatDeleteRace(t *testing.T) {
	t.Parallel()
	t.Run("identity changes around read", func(t *testing.T) {
		t.Parallel()
		fake := buildSnapshotTestHierarchy()
		stats := 0
		fake.beforeCall = func(op HierarchyOperation, rel string) error {
			if op == HierarchyOperationStat && rel == "primary" {
				stats++
				if stats == 2 {
					fake.bumpIdentity(rel)
				}
			}
			return nil
		}
		snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
			Purpose: ScanForPrecheck, ParentRel: "primary",
		}, NewBudgetTracker(ConvergenceBudget{}))
		if !errors.Is(err, ErrCgroupIdentityChanged) || snapshot != nil {
			t.Fatalf("snapshot=%v error=%v, want identity-changed failure", snapshot, err)
		}
	})

	t.Run("listed child disappears before stat", func(t *testing.T) {
		t.Parallel()
		fake := buildSnapshotTestHierarchy()
		fake.beforeCall = func(op HierarchyOperation, rel string) error {
			if op == HierarchyOperationStat && rel == "primary/pod-a" {
				delete(fake.nodes, rel)
			}
			return nil
		}
		snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
			Purpose: ScanForPrecheck, ParentRel: "primary",
		}, NewBudgetTracker(ConvergenceBudget{}))
		if !errors.Is(err, syscall.ENOENT) || snapshot != nil {
			t.Fatalf("snapshot=%v error=%v, want delete-race failure", snapshot, err)
		}
	})

	t.Run("parent rebuilt between read and list", func(t *testing.T) {
		t.Parallel()
		fake := buildSnapshotTestHierarchy()
		fake.beforeCall = func(op HierarchyOperation, rel string) error {
			if op == HierarchyOperationList && rel == "primary" {
				previous := fake.nodes[rel]
				fake.nodes[rel] = &fakeHierarchyNode{
					identity: CgroupIdentity{Device: previous.identity.Device, Inode: previous.identity.Inode + 100},
					cpus:     previous.cpus.Clone(),
					mems:     previous.mems,
				}
			}
			return nil
		}
		snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
			Purpose: ScanForPrecheck, ParentRel: "primary",
		}, NewBudgetTracker(ConvergenceBudget{}))
		if !errors.Is(err, ErrCgroupIdentityChanged) || snapshot != nil {
			t.Fatalf("snapshot=%v error=%v, want parent identity-changed failure and nil snapshot", snapshot, err)
		}
	})
}

func TestSnapshotRetirementRejectsControllerError(t *testing.T) {
	t.Parallel()
	assertSnapshotRetirementRejectsError(t, fmt.Errorf(
		"read cpuset.cpus: %w: %v", ErrCgroupControllerUnavailable, syscall.ENOENT))
}

func TestSnapshotRetirementRejectsPermissionError(t *testing.T) {
	t.Parallel()
	assertSnapshotRetirementRejectsError(t, syscall.EACCES)
}

func TestSnapshotRetirementRejectsENOTDIRAndENODEV(t *testing.T) {
	t.Parallel()
	for _, err := range []error{syscall.ENOTDIR, syscall.ENODEV} {
		err := err
		t.Run(err.Error(), func(t *testing.T) {
			t.Parallel()
			assertSnapshotRetirementRejectsError(t, err)
		})
	}
}

func TestSnapshotRetirementRejectsIOError(t *testing.T) {
	t.Parallel()
	assertSnapshotRetirementRejectsError(t, syscall.EIO)
}

func TestSnapshotRetirementRejectsDeadlineError(t *testing.T) {
	t.Parallel()
	assertSnapshotRetirementRejectsError(t, context.DeadlineExceeded)
}

func TestSnapshotRetirementRejectsBudgetError(t *testing.T) {
	t.Parallel()
	assertSnapshotRetirementRejectsError(t, ErrHierarchyIOOperationBudgetExceeded)
}

func TestSnapshotRetirementRejectsTextError(t *testing.T) {
	t.Parallel()
	assertSnapshotRetirementRejectsError(t, errors.New("read cpuset.cpus: no such file or directory"))
}

func TestSnapshotDoesNotSwallowChildNonAbsenceErrorWhenParentThenRetires(t *testing.T) {
	t.Parallel()
	fake := buildSnapshotTestHierarchy()
	parentRel := "primary/pod-a"
	parentIdentity := fake.nodes[parentRel].identity
	injected := fmt.Errorf("read child state: %w", syscall.EIO)
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == parentRel+"/container-a" {
			delete(fake.nodes, parentRel+"/container-a")
			delete(fake.nodes, parentRel)
			return injected
		}
		return nil
	}

	snapshot, err := buildCompleteSnapshot(
		context.Background(), fake, buildSnapshotTestDAG(t),
		SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary"}},
		NewBudgetTracker(ConvergenceBudget{}), nil,
		map[string]CgroupIdentity{parentRel: parentIdentity},
	)

	require.Nil(t, snapshot)
	require.ErrorIs(t, err, syscall.EIO)
	var snapshotErr *SnapshotError
	require.ErrorAs(t, err, &snapshotErr)
	require.Equal(t, HierarchyOperationRead, snapshotErr.Operation)
	require.Equal(t, parentRel+"/container-a", snapshotErr.Rel)
}

func TestSnapshotAuthorizedListAbsencePreservesErrorWhenParentStillPresent(t *testing.T) {
	t.Parallel()
	fake := buildSnapshotTestHierarchy()
	parentRel := "primary/pod-a"
	parentIdentity := fake.nodes[parentRel].identity
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationList && rel == parentRel {
			return syscall.ENOENT
		}
		return nil
	}

	snapshot, err := buildCompleteSnapshot(
		context.Background(), fake, buildSnapshotTestDAG(t),
		SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary"}},
		NewBudgetTracker(ConvergenceBudget{}), nil,
		map[string]CgroupIdentity{parentRel: parentIdentity},
	)

	require.Nil(t, snapshot)
	require.ErrorIs(t, err, syscall.ENOENT)
	var snapshotErr *SnapshotError
	require.ErrorAs(t, err, &snapshotErr)
	require.Equal(t, HierarchyOperationList, snapshotErr.Operation)
	require.Equal(t, parentRel, snapshotErr.Rel)
}

func TestSnapshotAuthorizedListAbsenceRejectsReplacement(t *testing.T) {
	t.Parallel()
	fake := buildSnapshotTestHierarchy()
	parentRel := "primary/pod-a"
	parentIdentity := fake.nodes[parentRel].identity
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationList && rel == parentRel {
			replacement := *fake.nodes[parentRel]
			replacement.identity.Inode++
			fake.nodes[parentRel] = &replacement
			return syscall.ENOENT
		}
		return nil
	}

	snapshot, err := buildCompleteSnapshot(
		context.Background(), fake, buildSnapshotTestDAG(t),
		SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary"}},
		NewBudgetTracker(ConvergenceBudget{}), nil,
		map[string]CgroupIdentity{parentRel: parentIdentity},
	)

	require.Nil(t, snapshot)
	require.ErrorIs(t, err, ErrCgroupIdentityChanged)
}

func TestSnapshotRetirementOnlyAcceptsENOENT(t *testing.T) {
	t.Parallel()

	require.True(t, isCgroupPathAbsent(syscall.ENOENT))
	require.True(t, isCgroupPathAbsent(fmt.Errorf("wrapped: %w", syscall.ENOENT)))
	require.False(t, isCgroupPathAbsent(syscall.ENOTDIR))
	require.False(t, isCgroupPathAbsent(syscall.ENODEV))
}

func TestSnapshotConfirmsEarlyRetirableChildAbsenceAndRejectsReplacement(t *testing.T) {
	t.Parallel()
	for _, firstFailure := range []HierarchyOperation{
		HierarchyOperationStat,
		HierarchyOperationRead,
	} {
		firstFailure := firstFailure
		t.Run(string(firstFailure), func(t *testing.T) {
			t.Parallel()
			fake := buildSnapshotTestHierarchy()
			childRel := "primary/pod-a"
			oldIdentity := fake.nodes[childRel].identity
			parentListed := false
			injected := false
			fake.beforeCall = func(op HierarchyOperation, rel string) error {
				if op == HierarchyOperationList && rel == "primary" {
					parentListed = true
				}
				if parentListed && !injected && op == firstFailure && rel == childRel {
					replacement := *fake.nodes[childRel]
					replacement.identity.Inode++
					fake.nodes[childRel] = &replacement
					injected = true
					return syscall.ENOENT
				}
				return nil
			}

			snapshot, err := buildCompleteSnapshot(
				context.Background(), fake, buildSnapshotTestDAG(t),
				SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary"}},
				NewBudgetTracker(ConvergenceBudget{}), nil,
				map[string]CgroupIdentity{childRel: oldIdentity},
			)

			require.Nil(t, snapshot)
			require.ErrorIs(t, err, ErrCgroupIdentityChanged)
		})
	}
}

func TestSnapshotRechecksParentChildrenAfterChildRetirement(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"same-name replacement", "membership drift"} {
		mutation := mutation
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			fake := buildSnapshotTestHierarchy()
			childRel := "primary/pod-a"
			childIdentity := fake.nodes[childRel].identity
			parentLists := 0
			fake.beforeCall = func(op HierarchyOperation, rel string) error {
				if op == HierarchyOperationList && rel == "primary" {
					parentLists++
					if parentLists == 2 {
						switch mutation {
						case "same-name replacement":
							fake.add(childRel, CgroupIdentity{
								Device: childIdentity.Device,
								Inode:  childIdentity.Inode + 100,
							}, "0-3", "0")
						case "membership drift":
							fake.add("primary/new-child", CgroupIdentity{
								Device: 1,
								Inode:  100,
							}, "0", "0")
						}
					}
				}
				if op == HierarchyOperationStat && rel == childRel {
					delete(fake.nodes, childRel)
					return syscall.ENOENT
				}
				return nil
			}

			snapshot, err := buildCompleteSnapshot(
				context.Background(), fake, buildSnapshotTestDAG(t),
				SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary"}},
				NewBudgetTracker(ConvergenceBudget{}), nil,
				map[string]CgroupIdentity{childRel: childIdentity},
			)

			require.Nil(t, snapshot)
			require.ErrorIs(t, err, ErrCgroupIdentityChanged)
			require.Equal(t, 2, parentLists)
		})
	}
}

func TestSnapshotParentRelistFiltersOutOfScopeSiblingsAfterChildRetirement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		parentRel    string
		retirableRel string
		boundaries   map[string]struct{}
	}{
		{
			name:         "controlled sibling",
			parentRel:    "reclaim",
			retirableRel: "reclaim/a-retirable",
		},
		{
			name:         "traversal boundary sibling",
			parentRel:    "primary",
			retirableRel: "primary/a-retirable",
			boundaries:   map[string]struct{}{"primary/pod-a": {}},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := buildSnapshotTestHierarchy()
			retirableIdentity := CgroupIdentity{Device: 1, Inode: 100}
			fake.add(tt.retirableRel, retirableIdentity, "0", "0")
			parentLists := 0
			fake.beforeCall = func(op HierarchyOperation, rel string) error {
				if op == HierarchyOperationList && rel == tt.parentRel {
					parentLists++
				}
				if op == HierarchyOperationStat && rel == tt.retirableRel {
					delete(fake.nodes, tt.retirableRel)
					return syscall.ENOENT
				}
				return nil
			}

			snapshot, err := buildCompleteSnapshot(
				context.Background(), fake, buildSnapshotTestDAG(t),
				SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{tt.parentRel}},
				NewBudgetTracker(ConvergenceBudget{}), tt.boundaries,
				map[string]CgroupIdentity{tt.retirableRel: retirableIdentity},
			)

			require.NoError(t, err)
			require.NotNil(t, snapshot)
			require.Equal(t, 2, parentLists)
			require.NotContains(t, snapshot.Entries, tt.retirableRel)
			require.Empty(t, snapshot.Children[tt.parentRel])
		})
	}
}

func assertSnapshotRetirementRejectsError(t *testing.T, injected error) {
	t.Helper()
	fake := buildSnapshotTestHierarchy()
	identity := fake.nodes["primary/pod-a"].identity
	parentListed := false
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationList && rel == "primary" {
			parentListed = true
		}
		if parentListed && op == HierarchyOperationStat && rel == "primary/pod-a" {
			return injected
		}
		return nil
	}

	snapshot, err := buildCompleteSnapshot(
		context.Background(), fake, buildSnapshotTestDAG(t),
		SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary"}},
		NewBudgetTracker(ConvergenceBudget{}), nil,
		map[string]CgroupIdentity{"primary/pod-a": identity},
	)

	require.Nil(t, snapshot)
	require.Error(t, err)
}

func TestSnapshotRejectsSymlinkWithDriverErrorAndNilResult(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	primary := filepath.Join(root, "primary")
	writeTestCgroupDirectory(t, primary)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(primary, "escape")); err != nil {
		t.Fatal(err)
	}
	rawDriver, err := NewCgroupV1Driver(root, nil)
	if err != nil {
		t.Fatalf("NewCgroupV1Driver() error = %v", err)
	}
	defer rawDriver.Close()
	driver := &stableIdentityTestDriver{HierarchyDriver: rawDriver}

	snapshot, err := BuildCompleteSnapshot(context.Background(), driver, buildSnapshotTestDAG(t), SnapshotRequest{
		Purpose: ScanForPlan, AffectedRels: []string{"primary"},
	}, NewBudgetTracker(ConvergenceBudget{}))
	if snapshot != nil {
		t.Fatalf("snapshot = %#v, want nil", snapshot)
	}
	var snapshotErr *SnapshotError
	if !errors.As(err, &snapshotErr) {
		t.Fatalf("error = %T %v, want *SnapshotError", err, err)
	}
	if snapshotErr.Operation != HierarchyOperationList {
		t.Fatalf("operation = %q, want %q: %v", snapshotErr.Operation, HierarchyOperationList, err)
	}
	if !strings.Contains(snapshotErr.Err.Error(), "symlink is not allowed") {
		t.Fatalf("driver error = %v, want symlink rejection propagated", snapshotErr.Err)
	}
}

func TestSnapshotDoesNotCrossControlledBoundary(t *testing.T) {
	t.Parallel()
	fake := buildSnapshotTestHierarchy()
	var listed []string
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationList {
			listed = append(listed, rel)
		}
		return nil
	}

	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
		Purpose:      ScanForPlan,
		AffectedRels: []string{"reclaim"},
	}, NewBudgetTracker(ConvergenceBudget{}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Entries["reclaim/bucket-0/pod-r"]; ok {
		t.Fatal("plan scan crossed into an unaffected controlled bucket")
	}
	if !reflect.DeepEqual(listed, []string{"reclaim"}) {
		t.Fatalf("listed = %v, want only affected root", listed)
	}
}

func TestSnapshotPreservesSelectedControlledChildEdge(t *testing.T) {
	t.Parallel()
	snapshot, err := BuildCompleteSnapshot(
		context.Background(),
		buildSnapshotTestHierarchy(),
		buildSnapshotTestDAG(t),
		SnapshotRequest{
			Purpose:      ScanForPlan,
			AffectedRels: []string{"reclaim/bucket-0"},
		},
		NewBudgetTracker(ConvergenceBudget{}),
	)
	require.NoError(t, err)
	require.Contains(t, snapshot.Entries, "reclaim")
	require.Contains(t, snapshot.Entries, "reclaim/bucket-0")
	require.Equal(t, []ChildRef{{
		Name:     "bucket-0",
		Identity: snapshot.Entries["reclaim/bucket-0"].Identity,
	}}, snapshot.Children["reclaim"])
}

func TestSnapshotNodeAndDepthBudgetFailWithoutPartialResult(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		budget ConvergenceBudget
		want   error
	}{
		{name: "node", budget: ConvergenceBudget{MaxSnapshotNodes: 2}, want: ErrNodeBudgetExceeded},
		{name: "depth", budget: ConvergenceBudget{MaxSnapshotDepth: 1}, want: ErrHierarchyDepthBudget},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tracker := NewBudgetTracker(tt.budget)
			driver := NewBudgetedHierarchyDriver(buildSnapshotTestHierarchy(), tracker)
			snapshot, err := BuildCompleteSnapshot(context.Background(), driver, buildSnapshotTestDAG(t), SnapshotRequest{
				Purpose: ScanForPlan, AffectedRels: []string{"primary"},
			}, tracker)
			if !errors.Is(err, tt.want) || snapshot != nil {
				t.Fatalf("snapshot=%v error=%v, want %v and nil snapshot", snapshot, err, tt.want)
			}
		})
	}
}

func TestSnapshotHierarchyIOBudgetFailsWithoutPartialResult(t *testing.T) {
	t.Parallel()
	tracker := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 1})
	snapshot, err := BuildCompleteSnapshot(
		context.Background(),
		buildSnapshotTestHierarchy(),
		buildSnapshotTestDAG(t),
		SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary"}},
		tracker,
	)
	if !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) || snapshot != nil {
		t.Fatalf("snapshot=%v error=%v, want hierarchy I/O budget error and nil snapshot", snapshot, err)
	}
}

func TestSnapshotStableExpandedNodeUsesFiveHierarchyCalls(t *testing.T) {
	t.Parallel()
	fake := newFakeHierarchyDriver()
	fake.add("root", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "root", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
		ControlledRoot: true,
	}})
	require.NoError(t, err)
	var calls []HierarchyOperation
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if rel == "root" {
			calls = append(calls, op)
		}
		return nil
	}

	_, err = BuildCompleteSnapshot(
		context.Background(), fake, dag,
		SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"root"}},
		NewBudgetTracker(ConvergenceBudget{}),
	)

	require.NoError(t, err)
	require.Equal(t, []HierarchyOperation{
		HierarchyOperationStat,
		HierarchyOperationRead,
		HierarchyOperationStat,
		HierarchyOperationList,
		HierarchyOperationStat,
	}, calls)
}

func TestSnapshotRetireSubtreeRemovesUnavailableOnlyDescendants(t *testing.T) {
	t.Parallel()
	holderIdentity := CgroupIdentity{Device: 1, Inode: 2}
	unavailableIdentity := CgroupIdentity{Device: 1, Inode: 3}
	snapshot := &CompleteSnapshot{
		Capabilities: v2Capabilities(),
		Entries: map[string]EntryState{
			"root": {
				Rel: "root", Identity: CgroupIdentity{Device: 1, Inode: 1},
				CPUs: machine.MustParse("0-1"), ConfiguredCPUs: machine.MustParse("0-1"),
			},
			"root/holder": {
				Rel: "root/holder", Identity: holderIdentity,
				CPUs: machine.NewCPUSet(1), ConfiguredCPUs: machine.NewCPUSet(1),
			},
		},
		Children: map[string][]ChildRef{
			"root": {{Name: "holder", Identity: holderIdentity}},
			"root/holder": {{
				Name: "unavailable", Identity: unavailableIdentity,
			}},
		},
		UnavailableChildren: map[string]UnavailableChildEvidence{
			"root/holder/unavailable": {
				Identity: unavailableIdentity,
				Reason:   UnavailableChildReasonControllerUnavailable,
			},
		},
		DomainByRel: map[string]DomainID{
			"root": DomainPrimary, "root/holder": DomainPrimary,
		},
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.MustParse("0-1"),
		},
		ScanBoundary: ScanBoundary{
			Purpose: ScanForPlan, Roots: []string{"root"},
			ExpandedRels: []string{"root", "root/holder"},
		},
	}
	require.NoError(t, validateCompleteSnapshotEvidence(snapshot))
	builder := &snapshotBuilder{
		snapshot: snapshot,
	}

	builder.retireSubtree("root/holder")
	builder.rebuildDomainUnion()

	require.NotContains(t, snapshot.UnavailableChildren, "root/holder/unavailable")
	require.NoError(t, validateCompleteSnapshotEvidence(snapshot))
}

func TestSnapshotPrecheckIncludesControlledImmediateChild(t *testing.T) {
	t.Parallel()
	snapshot, err := BuildCompleteSnapshot(
		context.Background(),
		buildSnapshotTestHierarchy(),
		buildSnapshotTestDAG(t),
		SnapshotRequest{Purpose: ScanForPrecheck, ParentRel: "reclaim"},
		NewBudgetTracker(ConvergenceBudget{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Entries["reclaim/bucket-0"]; !ok {
		t.Fatal("precheck snapshot omitted controlled immediate child")
	}
	if _, ok := snapshot.Entries["reclaim/bucket-0/pod-r"]; ok {
		t.Fatal("precheck snapshot expanded below immediate child")
	}
}

func TestSnapshotTraversalBoundaryChildIsExcludedFromChildren(t *testing.T) {
	t.Parallel()
	snapshot, err := buildCompleteSnapshot(
		context.Background(),
		buildSnapshotTestHierarchy(),
		buildSnapshotTestDAG(t),
		SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary"}},
		NewBudgetTracker(ConvergenceBudget{}),
		map[string]struct{}{"primary/pod-a": {}},
		nil,
	)

	require.NoError(t, err)
	require.NotContains(t, snapshot.Entries, "primary/pod-a")
	require.NotContains(t, snapshot.Children["primary"], ChildRef{
		Name:     "pod-a",
		Identity: CgroupIdentity{Device: 1, Inode: 2},
	})
}

func TestSnapshotAppliedViewExpandsMismatchOnly(t *testing.T) {
	t.Parallel()
	snapshot, err := BuildCompleteSnapshot(
		context.Background(),
		buildSnapshotTestHierarchy(),
		buildSnapshotTestDAG(t),
		SnapshotRequest{Purpose: ScanForAppliedView, MismatchRels: []string{"primary"}},
		NewBudgetTracker(ConvergenceBudget{}),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Entries["primary/pod-a/container-a"]; !ok {
		t.Fatal("applied-view snapshot did not expand mismatched primary subtree")
	}
	if _, ok := snapshot.Entries["reclaim/bucket-0/pod-r"]; ok {
		t.Fatal("applied-view snapshot expanded unrelated matching subtree")
	}
}

func TestSnapshotIDChangesWithIdentityChildrenCPUsMemsOrBoundary(t *testing.T) {
	t.Parallel()
	baseDriver := buildSnapshotTestHierarchy()
	base := mustBuildSnapshot(t, baseDriver, ScanForPlan, []string{"primary"})
	tests := []struct {
		name   string
		mutate func(*fakeHierarchyDriver)
		req    SnapshotRequest
	}{
		{name: "identity", mutate: func(f *fakeHierarchyDriver) { f.bumpIdentity("primary") }},
		{name: "children", mutate: func(f *fakeHierarchyDriver) {
			f.add("primary/pod-b", CgroupIdentity{Device: 1, Inode: 8}, "1", "0")
		}},
		{name: "cpus", mutate: func(f *fakeHierarchyDriver) { f.nodes["primary"].cpus = machine.MustParse("0") }},
		{name: "mems", mutate: func(f *fakeHierarchyDriver) { f.nodes["primary"].mems = "0-1" }},
		{name: "boundary", req: SnapshotRequest{Purpose: ScanForPrecheck, ParentRel: "primary"}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := buildSnapshotTestHierarchy()
			if tt.mutate != nil {
				tt.mutate(fake)
			}
			req := tt.req
			if req.Purpose == "" {
				req = SnapshotRequest{Purpose: ScanForPlan, AffectedRels: []string{"primary"}}
			}
			got, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), req, NewBudgetTracker(ConvergenceBudget{}))
			if err != nil {
				t.Fatal(err)
			}
			if got.ID == base.ID {
				t.Fatalf("snapshot ID did not change for %s", tt.name)
			}
		})
	}
}

func TestSnapshotIDChangesWithConfiguredCPUsOrMems(t *testing.T) {
	t.Parallel()
	base := &CompleteSnapshot{
		Entries: map[string]EntryState{
			"primary": {
				Rel: "primary", Identity: CgroupIdentity{Device: 1, Inode: 1},
				CPUs: machine.MustParse("0-3"), Mems: "0",
				ConfiguredCPUs: machine.MustParse("0-3"), ConfiguredMems: "0",
			},
		},
		DomainByRel:  map[string]DomainID{"primary": DomainPrimary},
		DomainUnion:  map[DomainID]machine.CPUSet{DomainPrimary: machine.MustParse("0-3")},
		ScanBoundary: ScanBoundary{Purpose: ScanForPlan, Roots: []string{"primary"}},
	}
	baseID := fingerprintSnapshot(base)

	for _, tc := range []struct {
		name   string
		mutate func(*CompleteSnapshot)
	}{
		{
			name: "configured cpus",
			mutate: func(snapshot *CompleteSnapshot) {
				entry := snapshot.Entries["primary"]
				entry.ConfiguredCPUs = machine.MustParse("1-2")
				snapshot.Entries["primary"] = entry
			},
		},
		{
			name: "configured mems",
			mutate: func(snapshot *CompleteSnapshot) {
				entry := snapshot.Entries["primary"]
				entry.ConfiguredMems = "1"
				snapshot.Entries["primary"] = entry
			},
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			changed := &CompleteSnapshot{
				Entries:      map[string]EntryState{"primary": base.Entries["primary"]},
				DomainByRel:  base.DomainByRel,
				DomainUnion:  base.DomainUnion,
				ScanBoundary: base.ScanBoundary,
			}
			tc.mutate(changed)
			if got := fingerprintSnapshot(changed); got == baseID {
				t.Fatalf("snapshot ID did not change for %s drift", tc.name)
			}
		})
	}
}

func TestSnapshotFingerprintSeparatesSectionBoundariesAndMapOwnership(t *testing.T) {
	t.Parallel()
	entry := EntryState{
		Rel: "a", Identity: CgroupIdentity{Device: 1, Inode: 1},
		CPUs: machine.NewCPUSet(0), ConfiguredCPUs: machine.NewCPUSet(0),
	}
	base := &CompleteSnapshot{
		Entries:      map[string]EntryState{"a": entry, "b": entry},
		Children:     map[string][]ChildRef{"a": {{Name: "x"}}, "b": {{Name: "y"}}},
		DomainByRel:  map[string]DomainID{"a": DomainPrimary, "b": DomainPrimary},
		ScanBoundary: ScanBoundary{Purpose: ScanForPlan, Roots: []string{"a"}, ExpandedRels: []string{"b"}},
	}
	tests := []struct {
		name   string
		mutate func(*CompleteSnapshot)
	}{
		{
			name: "children cannot move between parent keys",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.Children["a"] = []ChildRef{{Name: "x"}, {Name: "y"}}
				snapshot.Children["b"] = nil
			},
		},
		{
			name: "orphan children key is covered",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.Children["orphan"] = nil
			},
		},
		{
			name: "orphan domain key is covered",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.DomainByRel["orphan"] = DomainReclaim
			},
		},
		{
			name: "unavailable-child evidence is covered",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.UnavailableChildren = map[string]UnavailableChildEvidence{
					"a/x": {
						Identity: CgroupIdentity{Device: 1, Inode: 9},
						Reason:   UnavailableChildReasonControllerUnavailable,
					},
				}
			},
		},
		{
			name: "unavailable-child reason is covered",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.UnavailableChildren = map[string]UnavailableChildEvidence{
					"a/x": {
						Identity: CgroupIdentity{Device: 1, Inode: 9},
						Reason:   UnavailableChildReason("different-reason"),
					},
				}
			},
		},
	}

	baseID := fingerprintSnapshot(base)
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			changed := CloneCompleteSnapshot(base)
			tc.mutate(changed)
			if got := fingerprintSnapshot(changed); got == baseID {
				t.Fatalf("fingerprint collision: base=%x changed=%x", baseID, got)
			}
		})
	}
}

func TestSnapshotFingerprintIgnoresExpandedRelsDiagnostics(t *testing.T) {
	t.Parallel()
	snapshot := mustBuildSnapshot(
		t, buildSnapshotTestHierarchy(), ScanForPlan, []string{"primary"})
	changed := CloneCompleteSnapshot(snapshot)
	changed.ScanBoundary.ExpandedRels = []string{"diagnostic-only"}

	require.Equal(t, fingerprintSnapshot(snapshot), fingerprintSnapshot(changed))
}

func TestCloneCompleteSnapshotIsolatesUnavailableChildEvidence(t *testing.T) {
	t.Parallel()
	original := &CompleteSnapshot{
		UnavailableChildren: map[string]UnavailableChildEvidence{
			"root/child": {
				Identity: CgroupIdentity{Device: 1, Inode: 2},
				Reason:   UnavailableChildReasonControllerUnavailable,
			},
		},
	}
	cloned := CloneCompleteSnapshot(original)
	cloned.UnavailableChildren["root/child"] = UnavailableChildEvidence{
		Identity: CgroupIdentity{Device: 9, Inode: 9},
		Reason:   UnavailableChildReasonControllerUnavailable,
	}
	require.Equal(t, UnavailableChildEvidence{
		Identity: CgroupIdentity{Device: 1, Inode: 2},
		Reason:   UnavailableChildReasonControllerUnavailable,
	}, original.UnavailableChildren["root/child"])
}

func TestValidateCompleteSnapshotEvidenceRejectsInvalidUnavailableChildProof(t *testing.T) {
	t.Parallel()
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	valid := &CompleteSnapshot{
		Capabilities: v2Capabilities(),
		Entries: map[string]EntryState{
			"root": {Rel: "root", Identity: CgroupIdentity{Device: 1, Inode: 1}},
		},
		Children: map[string][]ChildRef{
			"root": {{Name: "child", Identity: childIdentity}},
		},
		UnavailableChildren: map[string]UnavailableChildEvidence{
			"root/child": {
				Identity: childIdentity,
				Reason:   UnavailableChildReasonControllerUnavailable,
			},
		},
		DomainByRel: map[string]DomainID{"root": DomainPrimary},
		DomainUnion: map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet()},
		ScanBoundary: ScanBoundary{
			Purpose: ScanForPlan, Roots: []string{"root"}, ExpandedRels: []string{"root"},
		},
	}
	require.NoError(t, validateCompleteSnapshotEvidence(valid))

	tests := []struct {
		name   string
		mutate func(*CompleteSnapshot)
	}{
		{
			name: "unsupported hierarchy",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.Capabilities.EffectiveCPUSet = false
			},
		},
		{
			name: "orphan evidence",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.UnavailableChildren["root/orphan"] = UnavailableChildEvidence{
					Identity: childIdentity,
					Reason:   UnavailableChildReasonControllerUnavailable,
				}
			},
		},
		{
			name: "identity mismatch",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.UnavailableChildren["root/child"] = UnavailableChildEvidence{
					Identity: CgroupIdentity{Device: 9, Inode: 9},
					Reason:   UnavailableChildReasonControllerUnavailable,
				}
			},
		},
		{
			name: "unexpected skip reason",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.UnavailableChildren["root/child"] = UnavailableChildEvidence{
					Identity: childIdentity,
					Reason:   UnavailableChildReason("permission-denied"),
				}
			},
		},
		{
			name: "entry and skip evidence overlap",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.Entries["root/child"] = EntryState{Rel: "root/child", Identity: childIdentity}
				snapshot.DomainByRel["root/child"] = DomainPrimary
			},
		},
		{
			name: "dangling child reference",
			mutate: func(snapshot *CompleteSnapshot) {
				delete(snapshot.UnavailableChildren, "root/child")
			},
		},
		{
			name: "child identity mismatch",
			mutate: func(snapshot *CompleteSnapshot) {
				delete(snapshot.UnavailableChildren, "root/child")
				snapshot.Entries["root/child"] = EntryState{
					Rel: "root/child", Identity: CgroupIdentity{Device: 9, Inode: 9},
				}
				snapshot.DomainByRel["root/child"] = DomainPrimary
			},
		},
		{
			name: "duplicate child name",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.Children["root"] = append(snapshot.Children["root"], snapshot.Children["root"][0])
			},
		},
		{
			name: "expanded rel without entry",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.ScanBoundary.ExpandedRels = append(
					snapshot.ScanBoundary.ExpandedRels, "root/missing")
			},
		},
		{
			name: "stale domain union",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.DomainUnion[DomainPrimary] = machine.NewCPUSet(9)
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			snapshot := CloneCompleteSnapshot(valid)
			tc.mutate(snapshot)
			before := fmt.Sprintf("%#v", snapshot)
			require.Error(t, validateCompleteSnapshotEvidence(snapshot))
			require.Equal(t, before, fmt.Sprintf("%#v", snapshot), "validation mutated snapshot")
		})
	}
}

func TestReleaseProofBoundaryReadsTrustAnchorsOnly(t *testing.T) {
	t.Parallel()
	fake := buildSnapshotTestHierarchy()
	var calls []string
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		calls = append(calls, string(op)+":"+rel)
		if strings.Contains(rel, "pod-r") {
			return errors.New("unrelated dynamic subtree must not be read")
		}
		return nil
	}

	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
		Purpose: ScanForWitness, SourceDomain: DomainReclaim,
	}, NewBudgetTracker(ConvergenceBudget{}))
	if err != nil {
		t.Fatalf("BuildCompleteSnapshot() error = %v calls=%v", err, calls)
	}
	for _, rel := range []string{"reclaim", "reclaim/bucket-0"} {
		if _, ok := snapshot.Entries[rel]; !ok {
			t.Errorf("witness snapshot missing trust anchor %q", rel)
		}
	}
	if _, ok := snapshot.Entries["reclaim/bucket-0/pod-r"]; ok {
		t.Fatal("witness snapshot read unrelated dynamic descendant")
	}
}

func buildSnapshotTestDAG(t *testing.T) *TopoDAG {
	t.Helper()
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, ControlledRoot: true, TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, ControlledRoot: true, TrustAnchor: true},
		{Rel: "reclaim/bucket-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, TrustAnchor: true, Constraint: TopologyConstraint{
			CPUUpperBound: machine.NewCPUSet(0),
			MemUpperBound: machine.NewCPUSet(0),
			Scope:         TopologyScopeNUMANode,
		}},
	})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	return dag
}

func buildSnapshotTestHierarchy() *fakeHierarchyDriver {
	fake := newFakeHierarchyDriver()
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-1", "0")
	fake.add("primary/pod-a", CgroupIdentity{Device: 1, Inode: 2}, "1", "0")
	fake.add("primary/pod-a/container-a", CgroupIdentity{Device: 1, Inode: 3}, "1", "0")
	fake.add("reclaim", CgroupIdentity{Device: 1, Inode: 4}, "2-3", "0")
	fake.add("reclaim/bucket-0", CgroupIdentity{Device: 1, Inode: 5}, "2-3", "0")
	fake.add("reclaim/bucket-0/pod-r", CgroupIdentity{Device: 1, Inode: 6}, "3", "0")
	return fake
}

func mustBuildSnapshot(t *testing.T, fake *fakeHierarchyDriver, purpose ScanPurpose, affected []string) *CompleteSnapshot {
	t.Helper()
	snapshot, err := BuildCompleteSnapshot(context.Background(), fake, buildSnapshotTestDAG(t), SnapshotRequest{
		Purpose: purpose, AffectedRels: affected,
	}, NewBudgetTracker(ConvergenceBudget{}))
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
