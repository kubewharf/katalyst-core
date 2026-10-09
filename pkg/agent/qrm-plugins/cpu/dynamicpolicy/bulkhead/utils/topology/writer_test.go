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
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

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

	version               cgroupclient.CgroupVersion
	cpus                  map[string]machine.CPUSet
	identities            map[string]CgroupIdentity
	children              map[string][]string
	files                 map[string]map[string][]byte
	reads                 int
	snapshotRootReads     int
	writes                []cpusetWrite
	failRel               map[string]bool
	applyErr              map[string]error
	readErr               map[string]error
	listErr               map[string]error
	onApply               func(rel string, data *cgcommon.CPUSetData)
	afterApply            func(rel string, data *cgcommon.CPUSetData)
	afterSnapshotRootRead func(reads int)

	enforceParentContainsTarget bool
	rejectEmptyCPUs             bool
}

type topologyFakeSnapshotDriver struct {
	cg *topologyFakeCgroup
}

type resetConvergenceStateDriver struct {
	HierarchyDriver
	states       map[string]EntryState
	capabilities HierarchyCapabilities
}

type expireAdjustmentAfterMemsDriver struct {
	HierarchyDriver
	adjustmentDone <-chan struct{}
	firstWrite     chan struct{}
	expired        bool
}

func (d *expireAdjustmentAfterMemsDriver) WriteMems(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
	mems string,
) error {
	if err := d.HierarchyDriver.WriteMems(ctx, rel, expected, mems); err != nil {
		return err
	}
	if !d.expired {
		d.expired = true
		if d.firstWrite != nil {
			close(d.firstWrite)
		}
		<-d.adjustmentDone
	}
	return nil
}

func (d *resetConvergenceStateDriver) ReadEntry(_ context.Context, rel string) (EntryState, error) {
	state, ok := d.states[rel]
	if !ok {
		return EntryState{}, syscall.ENOENT
	}
	return state, nil
}

func (d *resetConvergenceStateDriver) Capabilities() HierarchyCapabilities {
	return d.capabilities
}

func (d *resetConvergenceStateDriver) WriteCPUs(_ context.Context, rel string, expected CgroupIdentity, cpus machine.CPUSet) error {
	state, ok := d.states[rel]
	if !ok {
		return syscall.ENOENT
	}
	if state.Identity != expected {
		return ErrCgroupIdentityChanged
	}
	state.ConfiguredCPUs = cpus.Clone()
	if !cpus.IsEmpty() {
		state.CPUs = cpus.Clone()
	}
	d.states[rel] = state
	return nil
}

func (d *resetConvergenceStateDriver) WriteMems(_ context.Context, rel string, expected CgroupIdentity, mems string) error {
	state, ok := d.states[rel]
	if !ok {
		return syscall.ENOENT
	}
	if state.Identity != expected {
		return ErrCgroupIdentityChanged
	}
	state.Mems = mems
	state.ConfiguredMems = mems
	d.states[rel] = state
	return nil
}

func (f *topologyFakeCgroup) SnapshotDriver() HierarchyDriver {
	return &topologyFakeSnapshotDriver{cg: f}
}

func (d *topologyFakeSnapshotDriver) Close() error { return nil }

func (d *topologyFakeSnapshotDriver) Roots(context.Context) ([]RootRef, error) {
	return nil, nil
}

func (d *topologyFakeSnapshotDriver) StatIdentity(_ context.Context, rel string) (CgroupIdentity, error) {
	if identity, ok := d.cg.identities[rel]; ok {
		return identity, nil
	}
	return fakeSnapshotIdentity(rel), nil
}

func (d *topologyFakeSnapshotDriver) ReadEntry(ctx context.Context, rel string) (EntryState, error) {
	if rel == "primary" {
		d.cg.snapshotRootReads++
		if d.cg.afterSnapshotRootRead != nil {
			d.cg.afterSnapshotRootRead(d.cg.snapshotRootReads)
		}
	}
	cpus, err := d.cg.ReadCPUSet(ctx, rel)
	if err != nil {
		return EntryState{}, err
	}
	identity := fakeSnapshotIdentity(rel)
	if current, ok := d.cg.identities[rel]; ok {
		identity = current
	}
	mems := "0"
	if current, ok := d.cg.files[rel]["cpuset.mems"]; ok {
		mems = string(current)
	}
	return EntryState{Rel: rel, Identity: identity, CPUs: cpus, Mems: mems}, nil
}

func (d *topologyFakeSnapshotDriver) ReadEntryWithActivity(
	ctx context.Context,
	rel string,
	expected CgroupIdentity,
) (EntryState, error) {
	entry, err := d.ReadEntry(ctx, rel)
	if err != nil {
		return EntryState{}, err
	}
	if entry.Identity != expected {
		return EntryState{}, ErrCgroupIdentityChanged
	}
	tasksEmpty, err := membershipFileEmpty(d.cg.files[rel]["tasks"])
	if err != nil {
		return EntryState{}, err
	}
	procsEmpty, err := membershipFileEmpty(d.cg.files[rel]["cgroup.procs"])
	if err != nil {
		return EntryState{}, err
	}
	entry.Activity = CgroupActivity{TasksEmpty: tasksEmpty, CgroupProcsEmpty: procsEmpty, Childless: true}
	return entry, nil
}

func (d *topologyFakeSnapshotDriver) ListChildren(ctx context.Context, rel string) ([]ChildRef, error) {
	names, err := d.cg.ListChildren(ctx, rel)
	if err != nil {
		return nil, err
	}
	children := make([]ChildRef, 0, len(names))
	for _, name := range names {
		childRel := filepath.Join(rel, name)
		children = append(children, ChildRef{Name: name, Identity: fakeSnapshotIdentity(childRel)})
	}
	return children, nil
}

func (d *topologyFakeSnapshotDriver) WriteCPUs(ctx context.Context, rel string, expected CgroupIdentity, cpus machine.CPUSet) error {
	if d.cg.rejectEmptyCPUs && cpus.IsEmpty() {
		return fmt.Errorf("%w: rel=%q", ErrEmptyCPUSetUnsupported, rel)
	}
	current, err := d.StatIdentity(ctx, rel)
	if err != nil {
		return err
	}
	if current != expected {
		return ErrCgroupIdentityChanged
	}
	err = d.cg.ApplyCPUSet(ctx, rel, &cgcommon.CPUSetData{
		CPUs:           cpus.String(),
		WriteEmptyCPUs: cpus.IsEmpty(),
	})
	if err != nil {
		return fmt.Errorf("apply cpuset.cpus=%s @ %s: %w", cpus.String(), rel, err)
	}
	return nil
}

func (d *topologyFakeSnapshotDriver) WriteMems(ctx context.Context, rel string, expected CgroupIdentity, mems string) error {
	current, err := d.StatIdentity(ctx, rel)
	if err != nil {
		return err
	}
	if current != expected {
		return ErrCgroupIdentityChanged
	}
	if d.cg.files[rel] == nil {
		d.cg.files[rel] = make(map[string][]byte)
	}
	d.cg.files[rel]["cpuset.mems"] = []byte(mems)
	return nil
}

func (d *topologyFakeSnapshotDriver) Classify(err error, _ HierarchyOperation) HierarchyErrorClass {
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.EBUSY) || errors.Is(err, ErrCgroupIdentityChanged) {
		return HierarchyErrorStale
	}
	return HierarchyErrorInvalid
}

func (d *topologyFakeSnapshotDriver) Capabilities() HierarchyCapabilities {
	return HierarchyCapabilities{StableIdentity: true, KernelParentContainment: true}
}

func fakeSnapshotIdentity(rel string) CgroupIdentity {
	var inode uint64 = 1
	for _, value := range []byte(rel) {
		inode = inode*131 + uint64(value)
	}
	return CgroupIdentity{Device: 1, Inode: inode}
}

func TestVerifyResetConvergenceUsesConfiguredCPUsOnlyForV2EmptyTarget(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(), Mems: "0", TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	state := EntryState{
		Rel: "primary", Identity: CgroupIdentity{Device: 1, Inode: 1},
		CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.NewCPUSet(),
	}

	for _, tc := range []struct {
		name      string
		caps      HierarchyCapabilities
		converged bool
	}{
		{
			name:      "v2 empty configured means inherited effective state",
			caps:      HierarchyCapabilities{EmptyConfiguredCPUSet: true},
			converged: true,
		},
		{
			name:      "v1 keeps comparing effective state",
			caps:      HierarchyCapabilities{},
			converged: false,
		},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver := &resetConvergenceStateDriver{
				HierarchyDriver: newFakeHierarchyDriver(),
				states:          map[string]EntryState{"primary": state},
				capabilities:    tc.caps,
			}
			report, err := verifyResetConvergence(
				context.Background(), driver, NewBudgetTracker(ConvergenceBudget{}),
				dag, map[string]machine.CPUSet{"primary": machine.NewCPUSet()},
			)
			if err != nil {
				t.Fatalf("verifyResetConvergence() error = %v", err)
			}
			if report.FullyConverged != tc.converged {
				t.Fatalf("FullyConverged = %t, want %t; report=%+v", report.FullyConverged, tc.converged, report)
			}
		})
	}
}

func TestResetWriterSkipsUncontrolledDynamicDescendantWithoutCpusetController(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(), TrustAnchor: true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	fake := newFakeHierarchyDriver()
	fake.capabilities.EmptyConfiguredCPUSet = true
	fake.capabilities.EffectiveCPUSet = true
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	fake.add("primary/pod-a", CgroupIdentity{Device: 1, Inode: 2}, "0-3", "0")
	fake.add("primary/pod-a/init.scope", CgroupIdentity{Device: 1, Inode: 3}, "0-3", "0")
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if rel == "primary/pod-a/init.scope" &&
			(op == HierarchyOperationRead || op == HierarchyOperationWriteCPUs || op == HierarchyOperationWriteMems) {
			return ErrCgroupControllerUnavailable
		}
		return nil
	}

	res := &ConvergenceResult{}
	writer := newResetCoordinatorWriter(fake, NewBudgetTracker(ConvergenceBudget{}), "", res)
	err = writer.execute(context.Background(), dag, map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(),
	}, true, nil)
	// cgroup-v2 containment safety: emptying the controlled primary while the
	// unproven descendant still holds a non-empty cpuset must fail closed during
	// planning, before any write.
	if err == nil {
		t.Fatalf("expected fail-closed error for empty parent with non-empty unproven descendant, writes=%#v", fake.writes)
	}
	for _, write := range fake.writes {
		t.Fatalf("reset writer must not perform any write before failing closed; got=%#v", write)
	}
}

// TestResetWriterFailsClosedOnUnreadableUnprovenDescendant pins the read-error
// half of the cgroup-v2 containment gate: when emptying the controlled ancestor,
// an unproven descendant whose cpuset cannot be read for a reason other than the
// leaf having vanished or the cpuset controller being unattached must abort
// planning before any write. Failing open would let the empty-parent write
// silently clamp a leaf whose state we could not prove empty. Before the fix the
// generic read error was treated as "safe to proceed", so this test exercised the
// controlled primary write (err == nil, non-empty writes); it now must fail
// closed with zero writes.
func TestResetWriterFailsClosedOnUnreadableUnprovenDescendant(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(), TrustAnchor: true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	fake := newFakeHierarchyDriver()
	fake.capabilities.EmptyConfiguredCPUSet = true
	fake.capabilities.EffectiveCPUSet = true
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	// Unproven descendant listed under the controlled primary.
	fake.add("primary/pod-a", CgroupIdentity{Device: 1, Inode: 2}, "0-3", "0")
	transientReadErr := errors.New("simulated transient cgroup read failure")
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if rel == "primary/pod-a" && op == HierarchyOperationRead {
			return transientReadErr
		}
		return nil
	}

	res := &ConvergenceResult{}
	writer := newResetCoordinatorWriter(fake, NewBudgetTracker(ConvergenceBudget{}), "", res)
	err = writer.execute(context.Background(), dag, map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(),
	}, true, nil)
	if err == nil {
		t.Fatalf("expected fail-closed error for unreadable unproven descendant, writes=%#v", fake.writes)
	}
	if !strings.Contains(err.Error(), "cpuset is unreadable") {
		t.Fatalf("error = %v, want the unreadable-leaf fail-closed reason", err)
	}
	if !errors.Is(err, transientReadErr) {
		t.Fatalf("error = %v, want to wrap the underlying read error %v", err, transientReadErr)
	}
	// Planning aborts before any physical write.
	if len(fake.writes) != 0 {
		t.Fatalf("reset writer must not perform any write before failing closed; got=%#v", fake.writes)
	}
}

// TestResetWriterToleratesKnownSafeUnprovenDescendantReadError guards against
// over-correction: a genuinely absent leaf (its cgroup dir raced away) and a
// subtree whose cpuset controller is not attached have no cpuset that emptying
// the controlled ancestor could strand or clamp, so planning must proceed and the
// controlled node must still be reset.
func TestResetWriterToleratesKnownSafeUnprovenDescendantReadError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		err  error
	}{
		{name: "path_absent", err: syscall.ENOENT},
		{name: "controller_unavailable", err: ErrCgroupControllerUnavailable},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dag, err := BuildDAG([]NodeSpec{{
				Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
				CPUs: machine.NewCPUSet(), TrustAnchor: true,
			}})
			if err != nil {
				t.Fatal(err)
			}

			fake := newFakeHierarchyDriver()
			fake.capabilities.EmptyConfiguredCPUSet = true
			fake.capabilities.EffectiveCPUSet = true
			fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
			fake.add("primary/pod-a", CgroupIdentity{Device: 1, Inode: 2}, "0-3", "0")
			fake.beforeCall = func(op HierarchyOperation, rel string) error {
				if rel == "primary/pod-a" && op == HierarchyOperationRead {
					return tc.err
				}
				return nil
			}

			res := &ConvergenceResult{}
			writer := newResetCoordinatorWriter(fake, NewBudgetTracker(ConvergenceBudget{}), "", res)
			err = writer.execute(context.Background(), dag, map[string]machine.CPUSet{
				"primary": machine.NewCPUSet(),
			}, true, nil)
			if err != nil {
				t.Fatalf("known-safe read error %v must not fail closed; got err=%v writes=%#v", tc.err, err, fake.writes)
			}
			written := false
			for _, w := range fake.writes {
				if w.rel == "primary" {
					written = true
				}
			}
			if !written {
				t.Fatalf("controlled primary must still be reset after safe skip; writes=%#v", fake.writes)
			}
		})
	}
}

func TestResetWriterRejectsControlledNodeWithoutCpusetController(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(), TrustAnchor: true,
	}})
	if err != nil {
		t.Fatal(err)
	}

	fake := newFakeHierarchyDriver()
	fake.capabilities.EmptyConfiguredCPUSet = true
	fake.capabilities.EffectiveCPUSet = true
	fake.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	fake.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "primary" {
			return ErrCgroupControllerUnavailable
		}
		return nil
	}

	res := &ConvergenceResult{}
	writer := newResetCoordinatorWriter(fake, NewBudgetTracker(ConvergenceBudget{}), "", res)
	err = writer.execute(context.Background(), dag, map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(),
	}, true, nil)
	if !errors.Is(err, ErrCgroupControllerUnavailable) {
		t.Fatalf("reset writer error = %v, want ErrCgroupControllerUnavailable", err)
	}
}

func TestResetWriterOrdersDynamicSubtreeFromCurrentToTargetDirection(t *testing.T) {
	t.Parallel()
	const (
		bucket    = "numa-0"
		pod       = bucket + "/pod-a"
		container = pod + "/container-a"
	)
	tests := []struct {
		name          string
		currentBucket string
		currentPod    string
		currentCtr    string
		targetBucket  string
		targetPod     string
		targetCtr     string
		wantOrder     []string
	}{
		{
			name:          "shrink is full postorder",
			currentBucket: "0-3",
			currentPod:    "0-3",
			currentCtr:    "0-3",
			targetBucket:  "0-1",
			targetPod:     "0-1",
			targetCtr:     "0-1",
			wantOrder:     []string{container, pod, bucket},
		},
		{
			name:          "grow is preorder",
			currentBucket: "0",
			currentPod:    "0",
			currentCtr:    "0",
			targetBucket:  "0-2",
			targetPod:     "0-1",
			targetCtr:     "0-1",
			wantOrder:     []string{bucket, pod, container},
		},
		{
			name:          "mixed relations use deterministic dependencies",
			currentBucket: "0-2",
			currentPod:    "0-2",
			currentCtr:    "0-2",
			targetBucket:  "0-3",
			targetPod:     "0-1",
			targetCtr:     "0-1",
			wantOrder:     []string{bucket, container, pod},
		},
		{
			name:          "shrinking parent and growing descendants stay deterministic",
			currentBucket: "0-2",
			currentPod:    "0",
			currentCtr:    "0",
			targetBucket:  "0-1",
			targetPod:     "0-1",
			targetCtr:     "0-1",
			wantOrder:     []string{pod, container, bucket},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dag, err := BuildDAG([]NodeSpec{{
				Rel: bucket, Role: TopoNodeRoleReclaimNUMABucket,
				Domain: DomainReclaim, CPUs: machine.MustParse(tt.targetBucket),
			}})
			if err != nil {
				t.Fatal(err)
			}
			driver := newFakeHierarchyDriver()
			driver.allowUnwitnessedExpansion = true
			driver.add(bucket, CgroupIdentity{Device: 1, Inode: 1}, tt.currentBucket, "0")
			driver.add(pod, CgroupIdentity{Device: 1, Inode: 2}, tt.currentPod, "0")
			driver.add(container, CgroupIdentity{Device: 1, Inode: 3}, tt.currentCtr, "0")

			writer := newResetCoordinatorWriter(
				driver, NewBudgetTracker(ConvergenceBudget{}), "", &ConvergenceResult{})
			err = writer.execute(context.Background(), dag, map[string]machine.CPUSet{
				bucket: machine.MustParse(tt.targetBucket),
			}, false, map[string]machine.CPUSet{
				pod:       machine.MustParse(tt.targetPod),
				container: machine.MustParse(tt.targetCtr),
			})
			if err != nil {
				t.Fatalf("reset writer error = %v", err)
			}
			got := make([]string, 0, len(driver.writes))
			for _, write := range driver.writes {
				got = append(got, write.rel)
			}
			if !reflect.DeepEqual(got, tt.wantOrder) {
				t.Fatalf("write order = %v, want %v", got, tt.wantOrder)
			}
			seen := make(map[string]struct{}, len(got))
			for _, rel := range got {
				if _, ok := seen[rel]; ok {
					t.Fatalf("reset writer retried rel %q: %v", rel, got)
				}
				seen[rel] = struct{}{}
			}
		})
	}
}

func TestExecutionWithRollbackErrorPreservesExecutionAndReservationCauses(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	budget := NewAdjustmentBudget(ctx, ConvergenceBudget{MaxPlanOperations: 1})
	reservation, err := budget.ReserveExecution(ExecutionReservationCost{
		Rollback: PhysicalWriteCost{CPUSetWrites: 1},
	})
	if err != nil {
		t.Fatalf("ReserveExecution() error = %v", err)
	}
	cancel()
	reservationErr := reservation.RecordWriteAttempt(
		ctx, true, PhysicalWriteCost{CPUSetWrites: 1})
	stale := &PlanStaleError{Rel: "reclaim/numa-0", Resource: "write"}

	err = executionWithRollbackError(stale, reservationErr)

	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("combined error = %v, want execution stale cause", err)
	}
	if !errors.Is(err, ErrAdjustmentDeadlineExceeded) ||
		!errors.Is(err, context.Canceled) {
		t.Fatalf("combined error = %v, want rollback reservation deadline/context causes", err)
	}
	var gotStale *PlanStaleError
	if !errors.As(err, &gotStale) || gotStale != stale {
		t.Fatalf("combined error stale = %#v, want %#v", gotStale, stale)
	}
	var budgetErr *AdjustmentBudgetExceededError
	if !errors.As(err, &budgetErr) {
		t.Fatalf("combined error = %v, want typed rollback reservation error", err)
	}
}

func TestVerifyResetConvergenceKeepsEffectiveCPUsForNonEmptyTarget(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.MustParse("1-2"), Mems: "0", TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	driver := &resetConvergenceStateDriver{
		HierarchyDriver: newFakeHierarchyDriver(),
		states: map[string]EntryState{"primary": {
			Rel: "primary", Identity: CgroupIdentity{Device: 1, Inode: 1},
			CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.MustParse("1-2"),
		}},
		capabilities: HierarchyCapabilities{EmptyConfiguredCPUSet: true},
	}
	report, err := verifyResetConvergence(
		context.Background(), driver, NewBudgetTracker(ConvergenceBudget{}),
		dag, map[string]machine.CPUSet{"primary": machine.MustParse("1-2")},
	)
	if err != nil {
		t.Fatalf("verifyResetConvergence() error = %v", err)
	}
	if report.FullyConverged || len(report.NonConvergedTargets) != 1 {
		t.Fatalf("report = %+v, want effective CPU mismatch", report)
	}
	if got := report.NonConvergedTargets[0].Observed.String(); got != "0-3" {
		t.Fatalf("observed CPUs = %q, want effective 0-3", got)
	}
}

func TestSafeWriterV2EmptyConfiguredCPUWriteRecordsSuccessfulJournal(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	hierarchy := newFakeHierarchyDriver()
	hierarchy.add("primary", identity, "0-3", "0")
	driver := &resetConvergenceStateDriver{
		HierarchyDriver: hierarchy,
		states: map[string]EntryState{"primary": {
			Rel: "primary", Identity: identity,
			CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.MustParse("0-3"),
			Mems: "0", ConfiguredMems: "0",
		}},
		capabilities: cgroupV2Policy.capabilities(true),
	}
	plan := PhasePlan{
		ConvergenceID: "v2-empty-configured-journal",
		Kind:          PhaseDrain,
		Operations: []PlanOperation{{
			Rel:              "primary",
			ExpectedIdentity: identity,
			ExpectedCurrent:  CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0"},
			Target:           CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0"},
			Direction:        WriteShrink,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	result := &ConvergenceResult{}

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), result).
		execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("execute(empty configured CPUs) error = %v", err)
	}
	if result.Applied != 1 || len(result.Journal) != 1 {
		t.Fatalf("result = %+v, want one applied journal entry", result)
	}
	applied := result.Journal[0]
	if !applied.Observed.CPUs.IsEmpty() || !applied.Target.CPUs.IsEmpty() {
		t.Fatalf("journal observed/target CPUs = %q/%q, want empty/empty",
			applied.Observed.CPUs.String(), applied.Target.CPUs.String())
	}
	if !roundOutcomeMadeNetProgress(RoundOutcome{Journal: result.Journal}) {
		t.Fatal("verified empty configured CPU write must count as progress")
	}
}

func TestSafeWriterAdjustmentReservationFailurePerformsNoWrites(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	driver := newFakeHierarchyDriver()
	driver.add("primary", identity, "0-1", "0-1")
	plan := PhasePlan{
		ConvergenceID: "adjustment-reservation-failure",
		Kind:          PhaseDrain,
		Capabilities:  driver.Capabilities(),
		Operations: []PlanOperation{{
			Rel:              "primary",
			ExpectedIdentity: identity,
			ExpectedCurrent:  CPUSetTarget{CPUs: machine.MustParse("0-1"), Mems: "0-1"},
			Target:           CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
			Direction:        WriteShrink,
			OwnsMems:         true,
			WriteMems:        true,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	adjustmentBudget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 1,
	})
	adjustmentBudget.maxWrites = 3

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), &ConvergenceResult{}).
		withAdjustmentBudget(adjustmentBudget).
		execute(context.Background(), plan)

	if !errors.Is(err, ErrAdjustmentWriteBudgetExceeded) {
		t.Fatalf("execute() error = %v, want %v", err, ErrAdjustmentWriteBudgetExceeded)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("reservation failure performed writes: %#v", driver.writes)
	}
}

func TestSafeWriterRollsBackFirstWriteAfterAdjustmentDeadline(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	base := newFakeHierarchyDriver()
	base.add("primary", identity, "0-1", "0-1")
	initial := base.snapshot()
	adjustmentCtx := newTriggeredDeadlineContext(context.Background())
	driver := &expireAdjustmentAfterMemsDriver{
		HierarchyDriver: base,
		adjustmentDone:  adjustmentCtx.Done(),
		firstWrite:      make(chan struct{}),
	}
	go func() {
		<-driver.firstWrite
		adjustmentCtx.expire()
	}()
	plan := PhasePlan{
		ConvergenceID: "deadline-after-first-write",
		Kind:          PhaseDrain,
		Capabilities:  base.Capabilities(),
		Operations: []PlanOperation{{
			Rel:              "primary",
			ExpectedIdentity: identity,
			ExpectedCurrent:  CPUSetTarget{CPUs: machine.MustParse("0-1"), Mems: "0-1"},
			Target:           CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
			Direction:        WriteShrink,
			OwnsMems:         true,
			WriteMems:        true,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	adjustmentBudget := NewAdjustmentBudget(adjustmentCtx, ConvergenceBudget{
		MaxPlanOperations: 1,
	})
	result := &ConvergenceResult{}

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), result).
		withAdjustmentBudget(adjustmentBudget).
		execute(adjustmentCtx, plan)

	if !errors.Is(err, ErrAdjustmentDeadlineExceeded) ||
		!errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("execute() error = %v, want original adjustment deadline", err)
	}
	if got := base.snapshot(); !reflect.DeepEqual(got, initial) {
		t.Fatalf("rollback state = %#v, want initial %#v", got, initial)
	}
	if got := adjustmentBudget.CumulativeWrites(); got != 2 {
		t.Fatalf("cumulative writes = %d, want first write plus rollback", got)
	}
	if result.forwardWriteAttempts != 1 || result.rollbackWriteAttempts != 1 {
		t.Fatalf("write attempts = forward:%d rollback:%d, want 1/1",
			result.forwardWriteAttempts, result.rollbackWriteAttempts)
	}
}

func TestV1FourNUMAWriterSkipsDormantBucketsAndWritesActiveBuckets(t *testing.T) {
	t.Parallel()
	dag := mustPlanDAG(t, []NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("4-7"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0"},
		{Rel: "reclaim/numa-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "1"},
		{Rel: "reclaim/numa-2", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(4, 7), Mems: "2"},
		{Rel: "reclaim/numa-3", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(5, 6), Mems: "3"},
	})
	inactive := CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}
	driver := newFakeHierarchyDriver()
	driver.add("reclaim", CgroupIdentity{Device: 1, Inode: 1}, "0-7", "0-3")
	driver.add("reclaim/numa-0", CgroupIdentity{Device: 1, Inode: 2}, "0-7", "0")
	driver.add("reclaim/numa-1", CgroupIdentity{Device: 1, Inode: 3}, "0-7", "1")
	driver.add("reclaim/numa-2", CgroupIdentity{Device: 1, Inode: 4}, "4-5", "2")
	driver.add("reclaim/numa-3", CgroupIdentity{Device: 1, Inode: 5}, "6-7", "3")
	driver.nodes["reclaim/numa-0"].activity = inactive
	driver.nodes["reclaim/numa-1"].activity = inactive

	snapshot := planSnapshot(map[string]EntryState{
		"reclaim":        {Identity: driver.nodes["reclaim"].identity, CPUs: machine.MustParse("0-7"), Mems: "0-3"},
		"reclaim/numa-0": {Identity: driver.nodes["reclaim/numa-0"].identity, CPUs: machine.MustParse("0-7"), Mems: "0", Activity: inactive},
		"reclaim/numa-1": {Identity: driver.nodes["reclaim/numa-1"].identity, CPUs: machine.MustParse("0-7"), Mems: "1", Activity: inactive},
		"reclaim/numa-2": {Identity: driver.nodes["reclaim/numa-2"].identity, CPUs: machine.MustParse("4-5"), Mems: "2"},
		"reclaim/numa-3": {Identity: driver.nodes["reclaim/numa-3"].identity, CPUs: machine.MustParse("6-7"), Mems: "3"},
	}, map[DomainID]machine.CPUSet{DomainReclaim: machine.MustParse("0-7")})
	snapshot.DomainByRel = map[string]DomainID{
		"reclaim": DomainReclaim, "reclaim/numa-0": DomainReclaim,
		"reclaim/numa-1": DomainReclaim, "reclaim/numa-2": DomainReclaim,
		"reclaim/numa-3": DomainReclaim,
	}
	snapshot.Children = map[string][]ChildRef{
		"reclaim": {
			{Name: "numa-0", Identity: driver.nodes["reclaim/numa-0"].identity},
			{Name: "numa-1", Identity: driver.nodes["reclaim/numa-1"].identity},
			{Name: "numa-2", Identity: driver.nodes["reclaim/numa-2"].identity},
			{Name: "numa-3", Identity: driver.nodes["reclaim/numa-3"].identity},
		},
		"reclaim/numa-0": nil,
		"reclaim/numa-1": nil,
		"reclaim/numa-2": nil,
		"reclaim/numa-3": nil,
	}

	materialized := materializeTargets(dag, snapshot, false, desiredTargets(dag))
	plan, err := BuildPhasePlan(PhasePlanInput{
		Kind: PhaseDrain, DAG: dag, Snapshot: materialized.Snapshot,
		DesiredByRel:  materialized.PhysicalByRel,
		SemanticByRel: materialized.SemanticByRel,
		DormantRels:   materialized.DormantRels,
		AllowedCPUs:   machine.MustParse("0-7"),
		Capabilities:  cgroupV1Policy.capabilities(true),
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
			2: {NUMANodeID: 1}, 3: {NUMANodeID: 1},
			4: {NUMANodeID: 2}, 5: {NUMANodeID: 2},
			6: {NUMANodeID: 3}, 7: {NUMANodeID: 3},
		},
		Budget: NewBudgetTracker(ConvergenceBudget{}),
	})
	if err != nil {
		t.Fatalf("BuildPhasePlan() error = %v", err)
	}
	result := &ConvergenceResult{}
	if err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), result).
		execute(context.Background(), plan); err != nil {
		t.Fatalf("writer execute() error = %v", err)
	}

	writesByRel := make(map[string]int)
	for _, write := range driver.writes {
		writesByRel[write.rel]++
	}
	for _, rel := range []string{"reclaim/numa-0", "reclaim/numa-1"} {
		if writesByRel[rel] != 0 {
			t.Fatalf("dormant rel %q writes = %d, want zero", rel, writesByRel[rel])
		}
	}
	for _, rel := range []string{"reclaim/numa-2", "reclaim/numa-3"} {
		if writesByRel[rel] == 0 {
			t.Fatalf("active rel %q received no writer operation", rel)
		}
	}
	for i, trace := range driver.traces {
		if err := subsetInvariant(trace); err != nil {
			t.Fatalf("write step %d violated parent containment: %v", i, err)
		}
	}
}

func TestSafeWriterRevalidatesDormantProofsImmediatelyBeforeFirstMutation(t *testing.T) {
	t.Parallel()
	inactive := CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}
	primaryIdentity := CgroupIdentity{Device: 1, Inode: 1}
	dormantIdentity := CgroupIdentity{Device: 1, Inode: 2}
	driver := newFakeHierarchyDriver()
	driver.add("primary", primaryIdentity, "0", "0")
	driver.add("dormant", dormantIdentity, "0", "0")
	driver.nodes["dormant"].activity = CgroupActivity{
		TasksEmpty: false, CgroupProcsEmpty: true, Childless: true,
	}
	plan := PhasePlan{
		ConvergenceID: "dormant-proof-race",
		Kind:          PhaseExpand,
		Capabilities:  cgroupV1Policy.capabilities(true),
		Operations: []PlanOperation{{
			Rel: "primary", ExpectedIdentity: primaryIdentity,
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.MustParse("0-1"), Mems: "0"},
			Direction:       WriteGrow, OwnsMems: true,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	writer := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil)
	writer.dormantProofs = map[string]DormantLeafProof{
		"dormant": {
			Identity:     dormantIdentity,
			ObservedCPUs: machine.NewCPUSet(0),
		},
	}

	err := writer.execute(context.Background(), plan)
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("execute() error = %v, want stale dormant proof", err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("writes = %+v, want none before dormant proof revalidation", driver.writes)
	}
	if inactive == driver.nodes["dormant"].activity {
		t.Fatal("test setup did not make the dormant proof stale")
	}
}

func TestSafeWriterV2ConfiguredClearIgnoresEffectiveChildUnion(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	hierarchy := newFakeHierarchyDriver()
	hierarchy.add("primary", identity, "0-3", "0")
	hierarchy.add("primary/leaf", childIdentity, "0-3", "0")
	driver := &resetConvergenceStateDriver{
		HierarchyDriver: hierarchy,
		states: map[string]EntryState{
			"primary": {
				Rel: "primary", Identity: identity, CPUs: machine.MustParse("0-3"),
				ConfiguredCPUs: machine.MustParse("0-3"), Mems: "0", ConfiguredMems: "0",
			},
			"primary/leaf": {
				Rel: "primary/leaf", Identity: childIdentity, CPUs: machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(), Mems: "0", ConfiguredMems: "0",
			},
		},
		capabilities: cgroupV2Policy.capabilities(true),
	}
	plan := PhasePlan{
		ConvergenceID: "v2-configured-clear-with-effective-child",
		Kind:          PhaseDrain, Capabilities: cgroupV2Policy.capabilities(true),
		Operations: []PlanOperation{{
			Rel: "primary", ExpectedIdentity: identity,
			ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0"},
			Direction:       WriteShrink, OwnsMems: true,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID

	result := &ConvergenceResult{}
	if err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), result).
		execute(context.Background(), plan); err != nil {
		t.Fatalf("execute configured clear with effective child: %v", err)
	}
	if got := driver.states["primary"].ConfiguredCPUs; !got.IsEmpty() {
		t.Fatalf("configured CPUs = %s, want empty", got.String())
	}
	if result.Applied != 1 {
		t.Fatalf("result = %+v, want one applied clear", result)
	}
}

func TestSafeWriterV2ConfiguredClearWithMemsShrinkChecksLiveChildMems(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	hierarchy := newFakeHierarchyDriver()
	hierarchy.add("primary", identity, "0-3", "0-1")
	hierarchy.add("primary/leaf", childIdentity, "0-3", "1")
	driver := &resetConvergenceStateDriver{
		HierarchyDriver: hierarchy,
		states: map[string]EntryState{
			"primary": {
				Rel: "primary", Identity: identity, CPUs: machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(), Mems: "0-1", ConfiguredMems: "0-1",
			},
			"primary/leaf": {
				Rel: "primary/leaf", Identity: childIdentity, CPUs: machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(), Mems: "1", ConfiguredMems: "1",
			},
		},
		capabilities: cgroupV2Policy.capabilities(true),
	}
	plan := PhasePlan{
		ConvergenceID: "v2-configured-clear-mems-shrink",
		Kind:          PhaseDrain, Capabilities: cgroupV2Policy.capabilities(true),
		Operations: []PlanOperation{{
			Rel: "primary", ExpectedIdentity: identity,
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0-1"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0"},
			Direction:       WriteShrink, OwnsMems: true, WriteMems: true,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) || stale.Resource != "child_union_cpuset.mems" {
		t.Fatalf("execute() error = %v, want live child mems stale", err)
	}
	if got := driver.states["primary"].Mems; got != "0-1" {
		t.Fatalf("parent mems = %q, want no write", got)
	}
}

func TestSafeWriterV2ConfiguredClearWithMemsShrinkValidatesAfterChildWrite(t *testing.T) {
	t.Parallel()
	parentIdentity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	hierarchy := newFakeHierarchyDriver()
	hierarchy.add("primary", parentIdentity, "0-3", "0-1")
	hierarchy.add("primary/leaf", childIdentity, "0-3", "0-1")
	driver := &resetConvergenceStateDriver{
		HierarchyDriver: hierarchy,
		states: map[string]EntryState{
			"primary": {
				Rel: "primary", Identity: parentIdentity, CPUs: machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(), Mems: "0-1", ConfiguredMems: "0-1",
			},
			"primary/leaf": {
				Rel: "primary/leaf", Identity: childIdentity, CPUs: machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(), Mems: "0-1", ConfiguredMems: "0-1",
			},
		},
		capabilities: cgroupV2Policy.capabilities(true),
	}
	plan := PhasePlan{
		ConvergenceID: "v2-configured-clear-mems-valid-child-first",
		Kind:          PhaseDrain, Capabilities: cgroupV2Policy.capabilities(true),
		Operations: []PlanOperation{
			{
				Rel: "primary/leaf", ExpectedIdentity: childIdentity,
				ParentRel: "primary", ExpectedParentIdentity: parentIdentity,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0-1"},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0"},
				Direction:       WriteShrink, OwnsMems: true, WriteMems: true,
			},
			{
				Rel: "primary", ExpectedIdentity: parentIdentity,
				ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0-1"},
				Target:          CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0"},
				Direction:       WriteShrink, OwnsMems: true, WriteMems: true,
			},
		},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}

	if err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan); err != nil {
		t.Fatalf("execute() error = %v, want child-first mems shrink success", err)
	}
	if got := driver.states["primary"].Mems; got != "0" {
		t.Fatalf("parent mems = %q, want 0", got)
	}
}

func TestSafeWriterV2ConfiguredClearWithMemsGrowChecksLiveParentMems(t *testing.T) {
	t.Parallel()
	parentIdentity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	hierarchy := newFakeHierarchyDriver()
	hierarchy.add("primary", parentIdentity, "0-3", "0")
	hierarchy.add("primary/leaf", childIdentity, "0-3", "0")
	driver := &resetConvergenceStateDriver{
		HierarchyDriver: hierarchy,
		states: map[string]EntryState{
			"primary": {
				Rel: "primary", Identity: parentIdentity, CPUs: machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(), Mems: "0", ConfiguredMems: "0",
			},
			"primary/leaf": {
				Rel: "primary/leaf", Identity: childIdentity, CPUs: machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(), Mems: "0", ConfiguredMems: "0",
			},
		},
		capabilities: cgroupV2Policy.capabilities(true),
	}
	plan := PhasePlan{
		ConvergenceID: "v2-configured-clear-mems-grow",
		Kind:          PhaseExpand, Capabilities: cgroupV2Policy.capabilities(true),
		Operations: []PlanOperation{{
			Rel: "primary/leaf", ExpectedIdentity: childIdentity,
			ParentRel: "primary", ExpectedParentIdentity: parentIdentity,
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0-1"},
			Direction:       WriteGrow, OwnsMems: true, WriteMems: true,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) || stale.Resource != "parent_cpuset.mems" {
		t.Fatalf("execute() error = %v, want live parent mems stale", err)
	}
	if got := driver.states["primary/leaf"].Mems; got != "0" {
		t.Fatalf("child mems = %q, want no write", got)
	}
}

func TestSafeWriterFinalChildFreezeCoversCPUOnlyShrink(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	driver := newFakeHierarchyDriver()
	driver.add("root", identity, "0-3", "0")
	driver.add("root/child", childIdentity, "0-1", "0")
	plan := singleSafeWriterOperationPlan("cpu-only-shrink-freeze", PlanOperation{
		Rel: "root", ExpectedIdentity: identity,
		ExpectedChildren:   ChildrenFingerprint([]ChildRef{{Name: "child", Identity: childIdentity}}),
		ExpectedChildUnion: machine.MustParse("0-1"),
		ExpectedCurrent:    CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0"},
		Target:             CPUSetTarget{CPUs: machine.MustParse("0-2"), Mems: "0"},
		Direction:          WriteShrink,
	})
	listCalls := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationList && rel == "root" {
			listCalls++
			if listCalls == 3 {
				driver.add("root/late", CgroupIdentity{Device: 1, Inode: 3}, "3", "0")
			}
		}
		return nil
	}

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) || stale.Resource != "children" {
		t.Fatalf("execute() error = %v, want final child freeze stale", err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("CPU-only shrink child churn must fail before writes, got %#v", driver.writes)
	}
}

func TestSafeWriterFinalChildFreezeCoversGrow(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	driver := newFakeHierarchyDriver()
	driver.allowUnwitnessedExpansion = true
	driver.add("root", identity, "0-1", "0")
	driver.add("root/child", childIdentity, "0", "0")
	plan := singleSafeWriterOperationPlan("grow-freeze", PlanOperation{
		Rel: "root", ExpectedIdentity: identity,
		ExpectedChildren:   ChildrenFingerprint([]ChildRef{{Name: "child", Identity: childIdentity}}),
		ExpectedChildUnion: machine.NewCPUSet(0),
		ExpectedCurrent:    CPUSetTarget{CPUs: machine.MustParse("0-1"), Mems: "0"},
		Target:             CPUSetTarget{CPUs: machine.MustParse("0-2"), Mems: "0"},
		Direction:          WriteGrow,
	})
	listCalls := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationList && rel == "root" {
			listCalls++
			if listCalls == 3 {
				driver.add("root/late", CgroupIdentity{Device: 1, Inode: 3}, "0", "0")
			}
		}
		return nil
	}

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) || stale.Resource != "children" {
		t.Fatalf("execute() error = %v, want final child freeze stale", err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("grow child churn must fail before writes, got %#v", driver.writes)
	}
}

func TestSafeWriterLaterOperationChurnPreventsAllWrites(t *testing.T) {
	t.Parallel()
	firstIdentity := CgroupIdentity{Device: 1, Inode: 1}
	firstChildIdentity := CgroupIdentity{Device: 1, Inode: 2}
	secondIdentity := CgroupIdentity{Device: 1, Inode: 3}
	secondChildIdentity := CgroupIdentity{Device: 1, Inode: 4}
	driver := newFakeHierarchyDriver()
	driver.add("first", firstIdentity, "0-3", "0")
	driver.add("first/child", firstChildIdentity, "0-1", "0")
	driver.add("second", secondIdentity, "0-3", "0")
	driver.add("second/child", secondChildIdentity, "0-1", "0")
	plan := PhasePlan{
		ConvergenceID: "later-operation-churn",
		Kind:          PhaseDrain,
		Operations: []PlanOperation{
			{
				Rel: "first", ExpectedIdentity: firstIdentity,
				ExpectedChildren:   ChildrenFingerprint([]ChildRef{{Name: "child", Identity: firstChildIdentity}}),
				ExpectedChildUnion: machine.MustParse("0-1"),
				ExpectedCurrent:    CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0"},
				Target:             CPUSetTarget{CPUs: machine.MustParse("0-2"), Mems: "0"},
				Direction:          WriteShrink, OwnsMems: true, WriteMems: true,
			},
			{
				Rel: "second", ExpectedIdentity: secondIdentity,
				ExpectedChildren:   ChildrenFingerprint([]ChildRef{{Name: "child", Identity: secondChildIdentity}}),
				ExpectedChildUnion: machine.MustParse("0-1"),
				ExpectedCurrent:    CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0"},
				Target:             CPUSetTarget{CPUs: machine.MustParse("0-2"), Mems: "0"},
				Direction:          WriteShrink, OwnsMems: true, WriteMems: true,
			},
		},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}
	secondListCalls := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationList && rel == "second" {
			secondListCalls++
			if secondListCalls == 3 {
				driver.add("second/late", CgroupIdentity{Device: 1, Inode: 5}, "3", "0")
			}
		}
		return nil
	}

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) {
		t.Fatalf("execute() error = %v, want later-operation churn stale", err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("later-operation churn must reject the whole plan before writes, got %#v", driver.writes)
	}
}

func TestSafeWriterAtomicallyReservesFinalPreflightBeforeFirstWrite(t *testing.T) {
	t.Parallel()
	firstIdentity := CgroupIdentity{Device: 1, Inode: 1}
	secondIdentity := CgroupIdentity{Device: 1, Inode: 2}
	driver := newFakeHierarchyDriver()
	driver.add("first", firstIdentity, "0-3", "0")
	driver.add("second", secondIdentity, "0-3", "0")
	plan := PhasePlan{
		ConvergenceID: "atomic-final-preflight-reservation",
		Kind:          PhaseDrain,
		Operations: []PlanOperation{
			{
				Rel: "first", ExpectedIdentity: firstIdentity, ExpectedChildren: ChildrenFingerprint(nil),
				ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0"},
				Target:          CPUSetTarget{CPUs: machine.MustParse("0-2"), Mems: "0"},
				Direction:       WriteShrink,
			},
			{
				Rel: "second", ExpectedIdentity: secondIdentity, ExpectedChildren: ChildrenFingerprint(nil),
				ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("0-3"), Mems: "0"},
				Target:          CPUSetTarget{CPUs: machine.MustParse("0-2"), Mems: "0"},
				Direction:       WriteShrink,
			},
		},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}
	// The sizing scan costs four calls. Full-plan final preflight plus mutation
	// costs ten more; leave one call short so the atomic reservation must fail.
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 13})
	err := newSafeCPUSetWriter(driver, budget, nil).execute(context.Background(), plan)
	if !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("execute() error = %v, want atomic hierarchy I/O reservation failure", err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("reservation failure must happen before first write, got %#v", driver.writes)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 4 {
		t.Fatalf("hierarchy I/O usage = %d, want only four sizing-scan calls", got)
	}
}

func TestSafeWriterPrepaidDriverNeverFallsBackToOrdinaryCharging(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	driver := newFakeHierarchyDriver()
	driver.add("root", identity, "0-1", "0")
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 2})
	reserved, err := newStrictReservedHierarchyDriver(
		context.Background(), driver, budget, 1)
	if err != nil {
		t.Fatalf("newStrictReservedHierarchyDriver() error = %v", err)
	}
	if _, err := reserved.ReadEntry(context.Background(), "root"); err != nil {
		t.Fatalf("first prepaid ReadEntry() error = %v", err)
	}
	if _, err := reserved.ReadEntry(context.Background(), "root"); !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("second ReadEntry() error = %v, want prepaid exhaustion", err)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 1 {
		t.Fatalf("hierarchy I/O usage = %d, want reserved charge to remain 1", got)
	}
	if got := driver.calls; got != 1 {
		t.Fatalf("underlying calls = %d, want exhausted call blocked", got)
	}
}

func singleSafeWriterOperationPlan(convergenceID string, operation PlanOperation) PhasePlan {
	plan := PhasePlan{
		ConvergenceID: convergenceID,
		Kind:          PhaseDrain,
		Operations:    []PlanOperation{operation},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	return plan
}

func TestValidateLiveOperationDirectionRejectsMemsDirectionMismatch(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		direction WriteDirection
		current   string
		target    string
	}{
		{name: "declared grow but mems shrink", direction: WriteGrow, current: "0-1", target: "0"},
		{name: "declared shrink but mems grow", direction: WriteShrink, current: "0", target: "0-1"},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			operation := PlanOperation{
				Rel:             "root",
				ExpectedCurrent: CPUSetTarget{CPUs: machine.MustParse("0-1"), Mems: tc.current},
				Target:          CPUSetTarget{CPUs: machine.MustParse("0-1"), Mems: tc.target},
				Direction:       tc.direction,
				OwnsMems:        true,
				WriteMems:       true,
			}
			current := EntryState{CPUs: machine.MustParse("0-1"), Mems: tc.current}
			var stale *PlanStaleError
			if err := validateLiveOperationDirection(operation, current, HierarchyCapabilities{}); !errors.As(err, &stale) {
				t.Fatalf("validateLiveOperationDirection() error = %v, want direction mismatch stale", err)
			}
		})
	}
}

func TestSafeWriterV2ConfiguredClearWithMemsShrinkRejectsMalformedLiveChildMems(t *testing.T) {
	t.Parallel()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	hierarchy := newFakeHierarchyDriver()
	hierarchy.add("primary", identity, "0-3", "0-1")
	hierarchy.add("primary/leaf", childIdentity, "0-3", "0")
	hierarchy.nodes["primary/leaf"].mems = "bad"
	driver := &resetConvergenceStateDriver{
		HierarchyDriver: hierarchy,
		states: map[string]EntryState{
			"primary": {
				Rel: "primary", Identity: identity, CPUs: machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(), Mems: "0-1", ConfiguredMems: "0-1",
			},
			"primary/leaf": {
				Rel: "primary/leaf", Identity: childIdentity, CPUs: machine.MustParse("0-3"),
				ConfiguredCPUs: machine.NewCPUSet(), Mems: "bad", ConfiguredMems: "bad",
			},
		},
		capabilities: cgroupV2Policy.capabilities(true),
	}
	plan := PhasePlan{
		ConvergenceID: "v2-configured-clear-malformed-child-mems",
		Kind:          PhaseDrain, Capabilities: cgroupV2Policy.capabilities(true),
		Operations: []PlanOperation{{
			Rel: "primary", ExpectedIdentity: identity,
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0-1"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: "0"},
			Direction:       WriteShrink, OwnsMems: true, WriteMems: true,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	if err == nil {
		t.Fatal("execute() accepted malformed live child mems")
	}
	if got := driver.states["primary"].Mems; got != "0-1" {
		t.Fatalf("parent mems = %q, want fail-closed before write", got)
	}
}

func TestSafeWriterSkipsStableUncontrolledV2ChildWithoutCpusetController(t *testing.T) {
	t.Parallel()
	driver, plan, childIdentity := safeWriterUnavailableChildFixture()
	driver.capabilities.EffectiveCPUSet = true
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "root/dynamic" {
			return ErrCgroupControllerUnavailable
		}
		return nil
	}

	if err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan); err != nil {
		t.Fatalf("execute() error = %v, want stable uncontrolled child skipped", err)
	}
	if got := driver.nodes["root"].cpus.String(); got != "0-2" {
		t.Fatalf("root cpus = %q, want 0-2", got)
	}
	if got, err := driver.StatIdentity(context.Background(), "root/dynamic"); err != nil || got != childIdentity {
		t.Fatalf("dynamic child identity = %v, %v; want %v", got, err, childIdentity)
	}
}

func TestScanLiveChildrenOnceFailsClosedWithoutSkipProof(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		controlled      bool
		anchor          bool
		cgroupV1        bool
		identityChanged bool
		wantErr         error
	}{
		{name: "controlled", controlled: true, wantErr: ErrCgroupControllerUnavailable},
		{name: "anchor", anchor: true, wantErr: ErrCgroupControllerUnavailable},
		{name: "cgroup v1", cgroupV1: true, wantErr: ErrCgroupControllerUnavailable},
		{name: "identity changed", identityChanged: true, wantErr: ErrCgroupIdentityChanged},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			driver, plan, _ := safeWriterUnavailableChildFixture()
			driver.capabilities.EffectiveCPUSet = !tc.cgroupV1
			if tc.controlled {
				plan.ControlledRels = append(plan.ControlledRels, "root/dynamic")
			}
			if tc.anchor {
				plan.FailClosedRoots = append(plan.FailClosedRoots, "root/dynamic")
			}
			driver.beforeCall = func(op HierarchyOperation, rel string) error {
				if op != HierarchyOperationRead || rel != "root/dynamic" {
					return nil
				}
				if tc.identityChanged {
					driver.bumpIdentity(rel)
				}
				return ErrCgroupControllerUnavailable
			}

			_, err := scanLiveChildrenOnce(
				context.Background(), driver, plan.Operations[0], safeWriterFailClosedRels(plan))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("scanLiveChildrenOnce() error = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestSafeWriterFailClosedRootsDoNotFollowBaseMutation(t *testing.T) {
	t.Parallel()
	driver, plan, _ := safeWriterUnavailableChildFixture()
	driver.capabilities.EffectiveCPUSet = true
	plan.FailClosedRoots = []string{"root/dynamic"}
	plan.Base.ScanBoundary.Roots = []string{"root/dynamic"}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	originalPlanID := plan.PlanID

	plan.Base.ScanBoundary.Roots[0] = "other"
	if got := canonicalExecutionPlanID(plan); got != originalPlanID {
		t.Fatalf("base roots mutation changed canonical PlanID: got %q, want %q", got, originalPlanID)
	}
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "root/dynamic" {
			return ErrCgroupControllerUnavailable
		}
		return nil
	}

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	if !errors.Is(err, ErrCgroupControllerUnavailable) {
		t.Fatalf("execute() error = %v, want fail-closed root error", err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("base roots mutation allowed writes: %#v", driver.writes)
	}
}

func TestSafeWriterFailClosedRootsRaceWithBaseMutation(t *testing.T) {
	t.Parallel()
	_, plan, _ := safeWriterUnavailableChildFixture()
	plan.FailClosedRoots = []string{"root/dynamic"}
	plan.Base.ScanBoundary.Roots = []string{"root/dynamic"}
	plan.PlanID = canonicalExecutionPlanID(plan)

	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			select {
			case <-stop:
				return
			default:
				plan.Base.ScanBoundary.Roots[0] = "other"
				plan.Base.ScanBoundary.Roots[0] = "root/dynamic"
			}
		}
	}()
	for i := 0; i < 1000; i++ {
		if _, ok := safeWriterFailClosedRels(plan)["root/dynamic"]; !ok {
			t.Fatal("planned fail-closed root disappeared")
		}
		if got := canonicalExecutionPlanID(plan); got != plan.PlanID {
			t.Fatalf("canonical PlanID changed during base mutation: got %q, want %q", got, plan.PlanID)
		}
	}
	close(stop)
	<-done
}

func safeWriterUnavailableChildFixture() (*fakeHierarchyDriver, PhasePlan, CgroupIdentity) {
	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	driver := newFakeHierarchyDriver()
	driver.add("root", rootIdentity, "0-3", "0")
	driver.add("root/dynamic", childIdentity, "0", "0")
	base := planSnapshot(map[string]EntryState{
		"root": {
			Rel: "root", Identity: rootIdentity, CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0",
		},
		"root/dynamic": {
			Rel: "root/dynamic", Identity: childIdentity, CPUs: machine.NewCPUSet(0), Mems: "0",
		},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1, 2, 3)})
	base.ScanBoundary.Roots = []string{"root"}
	plan := PhasePlan{
		ConvergenceID:   "v2-unavailable-dynamic-child",
		Kind:            PhaseDrain,
		Base:            base,
		FailClosedRoots: []string{"root"},
		Capabilities:    cgroupV2Policy.capabilities(true),
		Operations: []PlanOperation{{
			Rel: "root", ExpectedIdentity: rootIdentity,
			ExpectedChildren:   ChildrenFingerprint([]ChildRef{{Name: "dynamic", Identity: childIdentity}}),
			ExpectedChildUnion: machine.NewCPUSet(0),
			ExpectedCurrent:    CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
			Target:             CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2), Mems: "0"},
			Direction:          WriteShrink,
			OwnsMems:           true,
			WriteMems:          true,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	return driver, plan, childIdentity
}

func testCPUDetails() machine.CPUDetails {
	details := machine.CPUDetails{}
	for cpu := 0; cpu <= 9; cpu++ {
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: 0}
	}
	for _, cpu := range []int{42, 43, 44, 45, 46, 47, 96, 138, 139, 140, 141, 142, 143} {
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: 0}
	}
	for _, cpu := range []int{95, 144, 191} {
		details[cpu] = machine.CPUTopoInfo{NUMANodeID: 1}
	}
	return details
}

func newTopologyFakeCgroup() *topologyFakeCgroup {
	return &topologyFakeCgroup{
		version:    cgroupclient.CgroupVersionV1,
		cpus:       map[string]machine.CPUSet{},
		identities: map[string]CgroupIdentity{},
		children:   map[string][]string{},
		files:      map[string]map[string][]byte{},
		failRel:    map[string]bool{},
		applyErr:   map[string]error{},
		readErr:    map[string]error{},
		listErr:    map[string]error{},
	}
}

func (f *topologyFakeCgroup) Version(context.Context) cgroupclient.CgroupVersion {
	return f.version
}

func (f *topologyFakeCgroup) ReadCPUSet(_ context.Context, rel string) (machine.CPUSet, error) {
	f.reads++
	if err := f.readErr[rel]; err != nil {
		return machine.NewCPUSet(), err
	}
	if cpus, ok := f.cpus[rel]; ok {
		return cpus.Clone(), nil
	}
	return machine.NewCPUSet(), nil
}

func (f *topologyFakeCgroup) ApplyCPUSet(_ context.Context, rel string, data *cgcommon.CPUSetData) error {
	if f.onApply != nil {
		f.onApply(rel, data)
	}
	if err := f.applyErr[rel]; err != nil {
		return err
	}
	if f.failRel[rel] {
		return fmt.Errorf("forced failure @ %s", rel)
	}
	var target machine.CPUSet
	writeCPUs := data.CPUs != "" || data.WriteEmptyCPUs
	if writeCPUs {
		target = machine.MustParse(data.CPUs)
	}
	if writeCPUs && f.enforceParentContainsTarget {
		parent := filepath.Dir(rel)
		if parent != "." && parent != rel {
			if parentCPUs, ok := f.cpus[parent]; ok && !target.IsSubsetOf(parentCPUs) {
				return fmt.Errorf("target %s is outside parent %s cpuset %s", target.String(), parent, parentCPUs.String())
			}
		}
	}
	if writeCPUs && (f.version != cgroupclient.CgroupVersionV2 || !target.IsEmpty()) {
		for _, child := range f.children[rel] {
			childRel := filepath.Join(rel, child)
			if childCPUs := f.cpus[childRel]; !childCPUs.IsEmpty() && !childCPUs.IsSubsetOf(target) {
				return fmt.Errorf("child %s cpuset %s is outside parent target %s", childRel, childCPUs.String(), target.String())
			}
		}
	}
	if writeCPUs {
		f.cpus[rel] = target.Clone()
	}
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
	if err := f.listErr[rel]; err != nil {
		return nil, err
	}
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

func TestTopologyCoordinatorConvergeResetModeUsesExpandOnlyPath(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)

	cg = newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
		Mode:   ResetModeGuard(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge reset mode: %v writes=%#v result=%+v", err, cg.writes, res)
	}
	if len(cg.writes) == 0 {
		t.Fatalf("reset mode should perform a write, result=%+v", res)
	}
	if !res.Converged {
		t.Fatalf("Converged = false, report=%+v", res.ConvergenceReport)
	}
	want := []cpusetWrite{{rel: "primary", cpus: "2-3"}}
	if !reflect.DeepEqual(cg.writes, want) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, want)
	}
}

func TestTopologyCoordinatorConvergeResetModeReportsVerifyMismatch(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		if rel == "primary" {
			cg.cpus[rel] = machine.NewCPUSet(0, 1)
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
		Mode:       ResetModeGuard(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge reset mode: %v writes=%#v result=%+v", err, cg.writes, res)
	}
	if res.Converged {
		t.Fatalf("Converged = true, want false")
	}
	if got := len(res.ConvergenceReport.NonConvergedTargets); got != 1 {
		t.Fatalf("non-converged target count = %d, want 1 report=%+v", got, res.ConvergenceReport)
	}
}

func TestRevalidateGrowAuthorizationRejectsChildGrowOutsideFreshParent(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(0), ControlledRoot: true, TrustAnchor: true},
		{Rel: "tiger", Role: TopoNodeRoleReclaimSibling, Domain: DomainReclaim, CPUs: machine.NewCPUSet(1), ControlledRoot: true, TrustAnchor: true},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	fresh := planSnapshot(map[string]EntryState{
		"primary":                    {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(), Mems: "0"},
		"tiger":                      {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0), Mems: "0"},
		"tiger/http2p.agent.service": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0), Mems: "0"},
	}, map[DomainID]machine.CPUSet{
		DomainPrimary: machine.NewCPUSet(),
		DomainReclaim: machine.NewCPUSet(0),
	})
	fresh.Children = map[string][]ChildRef{"tiger": {{Name: "http2p.agent.service"}}}
	fresh.DomainByRel = map[string]DomainID{
		"primary": DomainPrimary, "tiger": DomainReclaim, "tiger/http2p.agent.service": DomainReclaim,
	}

	round := &coordinatorRound{
		dag:         dag,
		targetByRel: map[string]machine.CPUSet{"primary": machine.NewCPUSet(), "tiger": machine.NewCPUSet(1)},
		cpuDetails:  machine.CPUDetails{0: {NUMANodeID: 0}, 1: {NUMANodeID: 0}},
		budget:      NewBudgetTracker(ConvergenceBudget{}),
		snapshotSource: func(context.Context) (*CompleteSnapshot, error) {
			return fresh, nil
		},
	}
	plan := PhasePlan{
		ConvergenceID: "test-convergence",
		Base:          fresh,
		Witnesses:     nil,
		TargetByRel: map[string]CPUSetTarget{
			"tiger":                      {CPUs: machine.NewCPUSet(1), Mems: "0"},
			"tiger/http2p.agent.service": {CPUs: machine.NewCPUSet(1), Mems: "0"},
		},
		Operations: []PlanOperation{{
			Rel:       "tiger/http2p.agent.service",
			ParentRel: "tiger",
			ExpectedCurrent: CPUSetTarget{
				CPUs: machine.NewCPUSet(0),
				Mems: "0",
			},
			Target:    CPUSetTarget{CPUs: machine.NewCPUSet(1), Mems: "0"},
			Direction: WriteGrow,
		}},
	}

	err = round.revalidateGrowAuthorization(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) {
		t.Fatalf("revalidateGrowAuthorization error = %v, want PlanStaleError", err)
	}
	if stale.Rel != "tiger/http2p.agent.service" || stale.Resource != "parent_cpuset" {
		t.Fatalf("stale error = %+v, want child parent_cpuset stale", stale)
	}
	if round.pendingSnapshot != fresh {
		t.Fatalf("pending snapshot = %p, want stale-triggering fresh snapshot %p", round.pendingSnapshot, fresh)
	}
}

func TestTopologyCoordinatorConvergeResetModeDeadlineDurationExpiresBeforeWrite_BitsUT(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
		Mode:   ResetModeGuard(),
		Budget: ConvergenceBudget{
			Deadline: time.Now().Add(-time.Second),
		},
	})
	if !errors.Is(err, ErrConvergenceDeadlineExceeded) {
		t.Fatalf("Converge error = %v, want ErrConvergenceDeadlineExceeded; result=%+v", err, res)
	}
	if cg.reads != 0 || len(cg.writes) != 0 {
		t.Fatalf("expired reset deadline must fail before hierarchy I/O, reads=%d writes=%#v", cg.reads, cg.writes)
	}
}

func TestVerifyResetConvergenceDeadlineExpiresBeforeRead_BitsUT(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	driver := newFakeHierarchyDriver()
	driver.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "2-3", "0")
	budget := NewBudgetTracker(ConvergenceBudget{
		MaxHierarchyIOOperations: 10,
		Deadline:                 time.Now().Add(-time.Second),
	})

	report, err := verifyResetConvergence(context.Background(), driver, budget, dag, desiredTargets(dag))
	if !errors.Is(err, ErrConvergenceDeadlineExceeded) {
		t.Fatalf("verifyResetConvergence error = %v, want ErrConvergenceDeadlineExceeded; report=%+v", err, report)
	}
	if driver.calls != 0 {
		t.Fatalf("expired verify deadline must fail before validation read, calls=%d", driver.calls)
	}
}

// TestTopologyCoordinatorConvergeResetWritesOnlyControlledNodes skips unproven
// external and reclaim-dynamic descendants: the disabled reset writes only the
// controlled primary/reclaim DAG roots and never fans the parent target out to an
// unproven external primary leaf or an unproven reclaim-domain pod child.
func TestTopologyCoordinatorConvergeResetWritesOnlyControlledNodes(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
		{Rel: "custom-reclaim-root/reclaimed-0", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2, 3), Mems: "0", Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.cpus["primary/child-a"] = machine.NewCPUSet(0, 1)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(2, 3)
	cg.cpus["custom-reclaim-root/reclaimed-0"] = machine.NewCPUSet(2, 3)
	cg.cpus["custom-reclaim-root/reclaimed-0/pod-x"] = machine.NewCPUSet(2)
	cg.children["primary"] = []string{"child-a"}
	cg.children["custom-reclaim-root"] = []string{"reclaimed-0"}
	cg.children["custom-reclaim-root/reclaimed-0"] = []string{"pod-x"}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
		Mode:   ResetModeGuard(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if !res.Converged {
		t.Fatalf("expected convergence, got %+v writes=%#v", res, cg.writes)
	}
	// Unproven descendants are never written: neither the external primary leaf nor
	// the reclaim-domain dynamic pod inherits the parent target.
	for _, write := range cg.writes {
		if write.rel == "primary/child-a" || write.rel == "custom-reclaim-root/reclaimed-0/pod-x" {
			t.Fatalf("unproven descendant %s must not be written; writes=%#v", write.rel, cg.writes)
		}
	}
	// Controlled DAG roots are still reset.
	controlled := map[string]bool{"primary": false, "custom-reclaim-root": false, "custom-reclaim-root/reclaimed-0": false}
	for _, write := range cg.writes {
		if _, ok := controlled[write.rel]; ok {
			controlled[write.rel] = true
		}
	}
	for rel, written := range controlled {
		if !written {
			t.Fatalf("controlled node %s was not reset; writes=%#v", rel, cg.writes)
		}
	}
}

func TestTopologyCoordinatorConvergeNormalModeRejectsEmptyCPUDetailsWithoutCgroupIO(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:    dag,
		Cgroup: cg,
		Mode:   NormalModeGuard(),
	})
	if err == nil || !strings.Contains(err.Error(), "empty CPUDetails") {
		t.Fatalf("expected empty CPUDetails error, got %v", err)
	}
	if cg.reads != 0 || len(cg.writes) != 0 {
		t.Fatalf("empty CPUDetails should not access cgroup, reads=%d writes=%#v", cg.reads, cg.writes)
	}
}

func TestTopologyCoordinatorConvergeBridgesDisjointReplacement(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2, 3), Mems: "0", Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1)
	cg.cpus["reclaim/numa-0"] = machine.NewCPUSet(0, 1)
	cg.children["reclaim"] = []string{"numa-0"}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"reclaim/numa-0": machine.NewCPUSet(2, 3),
		},
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v writes=%#v result=%+v", err, cg.writes, res)
	}
	if got, want := cg.cpus["reclaim"], machine.NewCPUSet(2, 3); !got.Equals(want) {
		t.Fatalf("reclaim cpuset = %s, want terminal target %s (writes=%#v result=%+v)", got.String(), want.String(), cg.writes, res)
	}
	if got, want := cg.cpus["reclaim/numa-0"], machine.NewCPUSet(2, 3); !got.Equals(want) {
		t.Fatalf("bucket cpuset = %s, want terminal target %s (writes=%#v result=%+v)", got.String(), want.String(), cg.writes, res)
	}
}

func TestTopologyCoordinatorConvergeShrinksIntersectionBeforeExpandingOverlapReplacement(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2, 3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1, 2)

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v", err)
	}
	if got, want := cg.cpus["primary"], machine.NewCPUSet(1, 2, 3); !got.Equals(want) {
		t.Fatalf("primary cpuset = %s, want terminal target %s; writes=%#v", got.String(), want.String(), cg.writes)
	}
	for _, write := range cg.writes {
		cpus, parseErr := machine.Parse(write.cpus)
		if parseErr != nil {
			t.Fatalf("parse write %#v: %v", write, parseErr)
		}
		if !cpus.IsSubsetOf(machine.NewCPUSet(0, 1, 2, 3)) {
			t.Fatalf("write escaped observed/desired envelope: %#v", write)
		}
	}
}

func TestTopologyCoordinatorConvergeShrinksBeforeExpands(t *testing.T) {
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

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v", err)
	}
	want := []cpusetWrite{
		{rel: "domain-a", cpus: "0"},
		{rel: "domain-b", cpus: "2-3"},
	}
	if !reflect.DeepEqual(cg.writes[:2], want) {
		t.Fatalf("writes = %#v, want prefix %#v", cg.writes, want)
	}
}

func TestTopologyCoordinatorConvergePreShrinksSiblingMovesBeforeTargetGrow(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 2, 6)},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4, 5, 99)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2, 99)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(3, 4, 5, 6)
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		overlap := cg.cpus["kubepods"].Intersection(cg.cpus["custom-reclaim-root"])
		if !overlap.IsEmpty() {
			t.Fatalf("overlap after write rel=%s cpus=%s: kubepods=%s custom-reclaim-root=%s overlap=%s writes=%#v",
				rel, data.CPUs, cg.cpus["kubepods"].String(), cg.cpus["custom-reclaim-root"].String(), overlap.String(), cg.writes)
		}
	}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}

	if got, want := cg.cpus["kubepods"], machine.NewCPUSet(0, 1, 2, 6); !got.Equals(want) {
		t.Fatalf("primary cpuset = %s, want terminal target %s; writes=%#v", got.String(), want.String(), cg.writes)
	}
	if got, want := cg.cpus["custom-reclaim-root"], machine.NewCPUSet(3, 4, 5); !got.Equals(want) {
		t.Fatalf("reclaim cpuset = %s, want machine-envelope target %s; writes=%#v", got.String(), want.String(), cg.writes)
	}
}

func TestTopologyCoordinatorConvergeExpandPreservesObservedUntilFinalConvergence(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(6)},
		{Rel: "kubepods/pod-a", CPUs: machine.NewCPUSet(6), ParentRel: "kubepods"},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(5, 99)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 99)
	cg.cpus["kubepods/pod-a"] = machine.NewCPUSet(6)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(5, 6)
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		overlap := cg.cpus["kubepods"].Intersection(cg.cpus["custom-reclaim-root"])
		if !overlap.IsEmpty() {
			t.Fatalf("overlap after write rel=%s cpus=%s: kubepods=%s custom-reclaim-root=%s overlap=%s writes=%#v",
				rel, data.CPUs, cg.cpus["kubepods"].String(), cg.cpus["custom-reclaim-root"].String(), overlap.String(), cg.writes)
		}
	}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	var primaryWrites []string
	for _, w := range cg.writes {
		if w.rel == "kubepods" {
			primaryWrites = append(primaryWrites, w.cpus)
		}
	}
	if got, want := primaryWrites, []string{"0", "0,6", "6"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("primary writes = %#v, want non-empty drain/bridge/final sequence %#v; writes=%#v",
			got, want, cg.writes)
	}
}

func TestTopologyCoordinatorConvergeWithCPUDetailsUsesDomainPipelineForCrossDomainTransfer(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(1, 2)

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 0},
			2: {NUMANodeID: 0},
		},
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v result=%+v writes=%#v", err, res, cg.writes)
	}
	wantWrites := []cpusetWrite{
		{rel: "custom-reclaim-root", cpus: "2"},
		{rel: "kubepods", cpus: "0-1"},
	}
	if !reflect.DeepEqual(cg.writes, wantWrites) {
		t.Fatalf("writes = %#v, want %#v", cg.writes, wantWrites)
	}
	if !res.Converged {
		t.Fatalf("Converged = false, report=%+v", res.ConvergenceReport)
	}
}

func TestTopologyCoordinatorConvergeAccumulatesSnapshotBudgetAcrossRounds(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	for i := 0; i < 2000; i++ {
		name := fmt.Sprintf("child-%04d", i)
		rel := filepath.Join("primary", name)
		cg.children["primary"] = append(cg.children["primary"], name)
		cg.cpus[rel] = machine.NewCPUSet(0)
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		Mems:       "0",
		CPUDetails: testCPUDetails(),
		Budget: ConvergenceBudget{
			MaxHierarchyIOOperations: 16384,
			DeadlineDuration:         -1,
		},
	})
	if !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("TopologyCoordinatorConverge error = %v, want cumulative hierarchy I/O budget failure; result=%+v", err, res)
	}
	if cg.snapshotRootReads < 2 {
		t.Fatalf("snapshot root reads = %d, want budget exhaustion in a later snapshot round", cg.snapshotRootReads)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("budget exhaustion must fail closed before hierarchy mutation, writes=%#v", cg.writes)
	}
}

func TestTopologyCoordinatorConvergeFailsClosedOnSnapshotDepthLimit(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	parent := "primary"
	for depth := 2; depth <= 17; depth++ {
		name := fmt.Sprintf("level-%02d", depth)
		rel := filepath.Join(parent, name)
		cg.children[parent] = []string{name}
		cg.cpus[rel] = machine.NewCPUSet(0)
		parent = rel
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		Mems:       "0",
		CPUDetails: testCPUDetails(),
	})
	if !errors.Is(err, ErrHierarchyDepthBudget) {
		t.Fatalf("TopologyCoordinatorConverge error = %v, want hierarchy depth budget failure; result=%+v", err, res)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("incomplete snapshot must fail closed before hierarchy mutation, writes=%#v", cg.writes)
	}
}

func TestTopologyCoordinatorConvergeReturnsObservedTargetDriftToAdjustmentOwner(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0)
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		if rel == "kubepods" {
			cg.cpus[rel] = machine.NewCPUSet(0)
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0",
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 0},
		},
		Budget: ConvergenceBudget{MaxRounds: 3},
	})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("TopologyCoordinatorConverge error = %T %v, want owner-visible stale; result=%+v writes=%#v",
			err, err, res, cg.writes)
	}
	if res.Converged {
		t.Fatalf("Converged = true, want false")
	}
	if res.State != ConvergenceStateNonConverged {
		t.Fatalf("State = %s, want non-converged; result=%+v", res.State, res)
	}
	if got := len(res.Rounds); got != 1 {
		t.Fatalf("rounds = %d, want stale not to consume fixed-point rounds; result=%+v", got, res)
	}
	if res.ReplanDisposition != ReplanNotAllowed ||
		res.FinalSnapshotCurrent || res.FinalSnapshot != nil {
		t.Fatalf("result=%+v, want no cross-attempt authority without an adjustment budget", res)
	}
}

func TestTopologyCoordinatorConvergeReturnsCPUWriteStaleToAdjustmentOwner(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.applyErr["primary"] = syscall.EBUSY
	attempts := 0
	cg.onApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel != "primary" {
			return
		}
		attempts++
		if attempts > 1 {
			delete(cg.applyErr, rel)
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, Mems: "0", CPUDetails: machine.CPUDetails{0: {}, 1: {}},
		AdjustmentBudget: NewAdjustmentBudget(context.Background(), ConvergenceBudget{
			MaxPlanOperations: 8,
		}),
	})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("Converge error=%v, want stale returned to adjustment owner; result=%+v", err, res)
	}
	if res.Converged || attempts != 1 {
		t.Fatalf("result=%+v attempts=%d, want one coordinator compile with no internal replan", res, attempts)
	}
	if got := len(res.Rounds); got != 1 || res.Rounds[0].Status != RoundStatusStale {
		t.Fatalf("rounds=%+v, want exactly one stale round", res.Rounds)
	}
	if res.ReplanDisposition != ReplanSafeFromVerifiedFinalState ||
		!res.FinalSnapshotCurrent || res.FinalSnapshot == nil {
		t.Fatalf("result=%+v, want verified-current physical snapshot disposition", res)
	}
}

func TestTopologyCoordinatorConvergePersistentCPUWriteEBUSYDoesNotConsumeFixedPointRounds(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.applyErr["primary"] = syscall.EBUSY
	attempts := 0
	cg.onApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == "primary" {
			attempts++
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, Mems: "0", CPUDetails: machine.CPUDetails{0: {}, 1: {}},
		Budget: ConvergenceBudget{MaxRounds: 3},
	})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("Converge error=%T %v, want owner-visible stale; result=%+v", err, err, res)
	}
	if res.State != ConvergenceStateNonConverged || attempts != 1 || len(res.Rounds) != 1 {
		t.Fatalf("result=%+v attempts=%d, want one compile independent of fixed-point round budget", res, attempts)
	}
	if res.ReplanDisposition != ReplanNotAllowed ||
		res.FinalSnapshotCurrent || res.FinalSnapshot != nil {
		t.Fatalf("result=%+v, want no cross-attempt authority without an adjustment budget", res)
	}
}

func TestTopologyCoordinatorConvergeDoesNotRetryInvalidCPUWriteError(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.applyErr["primary"] = syscall.EACCES
	attempts := 0
	cg.onApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == "primary" {
			attempts++
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, Mems: "0", CPUDetails: machine.CPUDetails{0: {}, 1: {}},
	})
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("Converge error=%T %v, want original EACCES; result=%+v", err, err, res)
	}
	var stale *PlanStaleError
	if errors.As(err, &stale) || attempts != 1 || len(res.Rounds) != 0 {
		t.Fatalf("error=%v attempts=%d rounds=%+v, want invalid failure without retry", err, attempts, res.Rounds)
	}
}

func TestTopologyCoordinatorConvergeGuardsSiblingGrowWhenSourceShrinkFails(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 2)},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4, 5, 99)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2, 99)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(3, 4, 5)
	cg.applyErr["kubepods"] = syscall.EBUSY
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		overlap := cg.cpus["kubepods"].Intersection(cg.cpus["custom-reclaim-root"])
		if !overlap.IsEmpty() {
			t.Fatalf("overlap after write rel=%s cpus=%s: kubepods=%s custom-reclaim-root=%s overlap=%s writes=%#v",
				rel, data.CPUs, cg.cpus["kubepods"].String(), cg.cpus["custom-reclaim-root"].String(), overlap.String(), cg.writes)
		}
	}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err == nil {
		t.Fatalf("expected source shrink error, got nil; writes=%#v", cg.writes)
	}
	for _, w := range cg.writes {
		if w.rel == "custom-reclaim-root" && strings.Contains(w.cpus, "99") {
			t.Fatalf("target sibling should not grow failed CPU 99; writes=%#v", cg.writes)
		}
	}
}

func TestTopologyCoordinatorConvergePreShrinksReclaimBeforePendingPrimaryGrow(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 2)},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4, 5, 6)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(3, 4, 5, 6)
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		overlap := cg.cpus["kubepods"].Intersection(cg.cpus["custom-reclaim-root"])
		if !overlap.IsEmpty() {
			t.Fatalf("overlap after write rel=%s cpus=%s: kubepods=%s custom-reclaim-root=%s overlap=%s writes=%#v",
				rel, data.CPUs, cg.cpus["kubepods"].String(), cg.cpus["custom-reclaim-root"].String(), overlap.String(), cg.writes)
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		PendingProtections: []PendingProtection{{
			ScopeRel: "kubepods/pod-pending",
			CPUs:     machine.NewCPUSet(6),
			PodUID:   "pod-pending",
		}},
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if !res.Converged {
		t.Fatalf("Converged = false, state=%s report=%+v writes=%#v", res.State, res.ConvergenceReport, cg.writes)
	}

	wantPrefix := []cpusetWrite{
		{rel: "custom-reclaim-root", cpus: "3-5"},
		{rel: "kubepods", cpus: "0-2,6"},
	}
	if len(cg.writes) < len(wantPrefix) {
		t.Fatalf("writes = %#v, want prefix %#v", cg.writes, wantPrefix)
	}
	if !reflect.DeepEqual(cg.writes[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("writes = %#v, want prefix %#v", cg.writes, wantPrefix)
	}
}

func TestTopologyCoordinatorConvergePreShrinksReclaimSiblingBeforePendingPrimaryGrow(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 2)},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4, 5)},
		{Rel: "aa", Role: TopoNodeRoleReclaimSibling, CPUs: machine.NewCPUSet(3, 4, 5, 6)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(3, 4, 5)
	cg.cpus["aa"] = machine.NewCPUSet(3, 4, 5, 6)
	cg.afterApply = func(rel string, data *cgcommon.CPUSetData) {
		overlap := cg.cpus["kubepods"].Intersection(cg.cpus["aa"])
		if !overlap.IsEmpty() {
			t.Fatalf("overlap after write rel=%s cpus=%s: kubepods=%s aa=%s overlap=%s writes=%#v",
				rel, data.CPUs, cg.cpus["kubepods"].String(), cg.cpus["aa"].String(), overlap.String(), cg.writes)
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		PendingProtections: []PendingProtection{{
			ScopeRel: "kubepods/pod-pending",
			CPUs:     machine.NewCPUSet(6),
			PodUID:   "pod-pending",
		}},
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if !res.Converged {
		t.Fatalf("Converged = false, state=%s report=%+v writes=%#v", res.State, res.ConvergenceReport, cg.writes)
	}

	wantPrefix := []cpusetWrite{
		{rel: "aa", cpus: "3-5"},
		{rel: "kubepods", cpus: "0-2,6"},
	}
	if len(cg.writes) < len(wantPrefix) {
		t.Fatalf("writes = %#v, want prefix %#v", cg.writes, wantPrefix)
	}
	if !reflect.DeepEqual(cg.writes[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("writes = %#v, want prefix %#v", cg.writes, wantPrefix)
	}
}

func TestTopologyCoordinatorRepairsReclaimNUMABucketOverlapToDisjointTargets(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(0, 1), Mems: "0-1"},
		{
			Rel:       "custom-reclaim-root/reclaimed-0",
			ParentRel: "custom-reclaim-root",
			Role:      TopoNodeRoleReclaimNUMABucket,
			CPUs:      machine.NewCPUSet(0),
			Mems:      "0",
			Constraint: TopologyConstraint{
				CPUUpperBound: machine.NewCPUSet(0),
				Scope:         TopologyScopeNUMANode,
			},
			Metadata: map[string]string{"numa": "0"},
		},
		{
			Rel:       "custom-reclaim-root/reclaimed-1",
			ParentRel: "custom-reclaim-root",
			Role:      TopoNodeRoleReclaimNUMABucket,
			CPUs:      machine.NewCPUSet(1),
			Mems:      "1",
			Constraint: TopologyConstraint{
				CPUUpperBound: machine.NewCPUSet(1),
				Scope:         TopologyScopeNUMANode,
			},
			Metadata: map[string]string{"numa": "1"},
		},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	cg := newTopologyFakeCgroup()
	cg.rejectEmptyCPUs = true
	cg.enforceParentContainsTarget = true
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(0)
	cg.cpus["custom-reclaim-root/reclaimed-0"] = machine.NewCPUSet(0)
	cg.cpus["custom-reclaim-root/reclaimed-1"] = machine.NewCPUSet(0)
	cg.files["custom-reclaim-root"] = map[string][]byte{"cpuset.mems": []byte("0-1")}
	cg.files["custom-reclaim-root/reclaimed-0"] = map[string][]byte{"cpuset.mems": []byte("0")}
	cg.files["custom-reclaim-root/reclaimed-1"] = map[string][]byte{"cpuset.mems": []byte("1")}
	cg.children["custom-reclaim-root"] = []string{"reclaimed-0", "reclaimed-1"}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		Mems:       "0-1",
		CPUDetails: machine.CPUDetails{0: {NUMANodeID: 0}, 1: {NUMANodeID: 1}},
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"custom-reclaim-root":             machine.NewCPUSet(0, 1),
			"custom-reclaim-root/reclaimed-0": machine.NewCPUSet(0),
			"custom-reclaim-root/reclaimed-1": machine.NewCPUSet(1),
		},
		Budget: ConvergenceBudget{
			MaxRounds: 8,
		},
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; result=%+v writes=%#v", err, res, cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root/reclaimed-0"]; !got.Equals(machine.NewCPUSet(0)) {
		t.Fatalf("reclaimed-0 cpuset = %s, want 0; writes=%#v", got.String(), cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root/reclaimed-1"]; !got.Equals(machine.NewCPUSet(1)) {
		t.Fatalf("reclaimed-1 cpuset = %s, want 1; writes=%#v", got.String(), cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root"]; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("custom-reclaim-root cpuset = %s, want 0-1; writes=%#v", got.String(), cg.writes)
	}
	if overlap := cg.cpus["custom-reclaim-root/reclaimed-0"].Intersection(cg.cpus["custom-reclaim-root/reclaimed-1"]); !overlap.IsEmpty() {
		t.Fatalf("reclaim NUMA buckets overlap: %s; writes=%#v", overlap.String(), cg.writes)
	}
}

func TestTopologyCoordinatorConvergeValidationAndFailurePaths(t *testing.T) {
	t.Parallel()

	if _, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{}); err == nil {
		t.Fatalf("expected nil DAG error")
	}
	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	if _, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{DAG: dag}); err == nil {
		t.Fatalf("expected nil cgroup error")
	}

	cg := newTopologyFakeCgroup()
	cg.failRel["primary"] = true
	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mode:       ResetModeGuard(),
	})
	if err == nil {
		t.Fatalf("expected apply error")
	}
	if res.Failed == 0 {
		t.Fatalf("expected failed count, got %+v", res)
	}
}

func TestTopologyCoordinatorConvergeExpandsUnmanagedDescendants(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.children["primary"] = []string{"burstable"}
	cg.children["primary/burstable"] = []string{"pod"}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v", err)
	}
	if res.Applied == 0 {
		t.Fatalf("expected descendant writes, got %+v", res)
	}
	if got := cg.cpus["primary/burstable/pod"].String(); got != "0-1" {
		t.Fatalf("planned dynamic leaf cpuset = %s, want parent target 0-1; writes=%#v", got, cg.writes)
	}
}

// TestTopologyCoordinatorConvergeRejectsEmptyTargetWithNonEmptyUnmanagedDescendantV2
// pins the cgroup-v2 containment contract: a controlled primary root may not be
// emptied while an unproven external descendant still holds a non-empty cpuset.
// The reset must fail closed during planning, before any write, and must not be
// marked complete.
func TestTopologyCoordinatorConvergeRejectsEmptyTargetWithNonEmptyUnmanagedDescendantV2(t *testing.T) {
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

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mode:       ResetModeGuard(),
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"primary/burstable/pod-a/container-a": machine.NewCPUSet(0),
		},
	})
	if err == nil {
		t.Fatalf("expected fail-closed error, got res=%+v writes=%#v", res, cg.writes)
	}
	// No partial writes: the reset aborts before touching any cgroup.
	if len(cg.writes) != 0 {
		t.Fatalf("expected no writes on fail-closed reset, got=%#v", cg.writes)
	}
}

func TestTopologyCoordinatorConvergeExpandsEmptyTargetsWithProtectKubeLeafV2(t *testing.T) {
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

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mode:       ResetModeGuard(),
	})
	// Emptying the controlled kubepods root while unproven descendants still hold a
	// non-empty cpuset is unsafe: fail closed before any write, do not fan out.
	if err == nil {
		t.Fatalf("expected fail-closed error, got res=%+v writes=%#v", res, cg.writes)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("expected no writes on fail-closed reset, got=%#v", cg.writes)
	}
}

func TestTopologyCoordinatorConvergeSkipsEmptyTargetsV1(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet()}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v", err)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("empty v1 target should not be written, got %#v", cg.writes)
	}
	if res.Skipped != 0 || res.Attempted != 0 {
		t.Fatalf("unchanged empty v1 target should need no operation, got %+v", res)
	}
}

func TestTopologyCoordinatorConvergeConvergesExistingKubeLeavesBeforePrimaryShrink(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2)},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4, 5)},
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
	cg.cpus["custom-reclaim-root"] = full.Clone()
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-abc"}
	cg.children["kubepods/burstable/pod-abc"] = []string{"container-a"}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
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
	if got := cg.cpus["custom-reclaim-root"].String(); got != "3-5" {
		t.Fatalf("reclaim cpuset = %s, want 3-5; writes=%#v", got, cg.writes)
	}
}

func TestTopologyCoordinatorConvergeExpandsKubeIntermediateBeforeConvergingLiveLeaves(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2, 3)},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(4, 5)},
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
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(4, 5)
	cg.children["kubepods"] = []string{"burstable"}
	cg.children["kubepods/burstable"] = []string{"pod-abc"}
	cg.children["kubepods/burstable/pod-abc"] = []string{"container-a"}
	cg.files["kubepods/burstable/pod-abc/container-a"] = map[string][]byte{"tasks": []byte("123\n")}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubepods"].String(); got != "1-3" {
		t.Fatalf("primary cpuset = %s, want 1-3; writes=%#v", got, cg.writes)
	}
	for _, rel := range []string{
		"kubepods/burstable",
		"kubepods/burstable/pod-abc",
		"kubepods/burstable/pod-abc/container-a",
	} {
		if got := cg.cpus[rel].String(); got != "1-3" {
			t.Fatalf("planned dynamic descendant cpuset @ %s = %s, want 1-3; writes=%#v", rel, got, cg.writes)
		}
	}
}

func TestTopologyCoordinatorConvergePlansKubePodLeafInImmediateExpandClosure(t *testing.T) {
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

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v", err)
	}
	if got := cg.cpus["kubepods"].String(); got != "0-1" {
		t.Fatalf("primary target = %s, want 0-1 without unmanaged leaf widening; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "0-1" {
		t.Fatalf("dynamic leaf cpuset = %s, want controlled closure target 0-1; writes=%#v", got, cg.writes)
	}
	wroteLeaf := false
	for _, w := range cg.writes {
		if w.rel == "kubepods/burstable/pod-abc/container-a" {
			wroteLeaf = true
		}
	}
	if !wroteLeaf {
		t.Fatalf("dynamic leaf must be an explicit plan member; writes=%#v", cg.writes)
	}
	_ = res
}

func TestTopologyCoordinatorConvergePlansKubePauseLeafInImmediateExpandClosure(t *testing.T) {
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

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v", err)
	}
	if got := cg.cpus["kubepods"].String(); got != "0-1" {
		t.Fatalf("primary target = %s, want 0-1 without unmanaged pause widening; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubepods/besteffort/pod-abc"].String(); got != "0-1" {
		t.Fatalf("dynamic pause leaf cpuset = %s, want controlled closure target 0-1; writes=%#v", got, cg.writes)
	}
	wroteLeaf := false
	for _, w := range cg.writes {
		if w.rel == "kubepods/besteffort/pod-abc" {
			wroteLeaf = true
		}
	}
	if !wroteLeaf {
		t.Fatalf("dynamic pause leaf must be an explicit plan member; writes=%#v", cg.writes)
	}
	_ = res
}

// TestTopologyCoordinatorConvergeProtectStillWritesExpectedLeaf verifies protection does not
// suppress writes for container leaves that ARE present in ExpectedCPUSetByRel:
// those still get their resolved allocation enforced.
func TestTopologyCoordinatorConvergeProtectStillWritesExpectedLeaf(t *testing.T) {
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

	if _, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"kubepods/burstable/pod-abc/container-a": machine.NewCPUSet(1, 2),
		},
	}); err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v", err)
	}
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "1-2" {
		t.Fatalf("explicit expected leaf cpuset = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	for _, rel := range []string{"kubepods/burstable", "kubepods/burstable/pod-abc"} {
		if got := cg.cpus[rel].String(); got != "0-2" {
			t.Fatalf("planned intermediate %s cpuset = %s, want parent envelope 0-2; writes=%#v", rel, got, cg.writes)
		}
	}
}

// TestTopologyCoordinatorConvergeReleasesUnmanagedLeafWithoutProtect verifies the reset/widen
// path (protection disabled) still propagates the parent target onto an
// unmanaged leaf, which is how a polluted leaf recovers back to a wide cpuset.
func TestTopologyCoordinatorConvergeReleasesUnmanagedLeafWithoutProtect(t *testing.T) {
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

	if _, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	}); err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v", err)
	}
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "0-6" {
		t.Fatalf("planned dynamic leaf cpuset = %s, want parent target 0-6; writes=%#v", got, cg.writes)
	}
}

func TestTopologyCoordinatorConvergeShrinkFallbackRelistsLiveChildrenAfterCacheMiss(t *testing.T) {
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

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err == nil {
		t.Fatalf("expected live-child race to block the parent shrink; writes=%#v", cg.writes)
	}
	if got := cg.cpus["primary/late-child"].String(); got != "0-3" {
		t.Fatalf("late child cpuset = %s, want unchanged 0-3; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["primary"].String(); got != "0-3" {
		t.Fatalf("primary cpuset = %s, want unchanged 0-3 after live-child race; writes=%#v", got, cg.writes)
	}
}

func TestTopologyCoordinatorConvergePlansExpectedLeafTarget(t *testing.T) {
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

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"kubepods/burstable/pod-abc/container-a": machine.NewCPUSet(1, 2),
		},
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v", err)
	}
	if got := cg.cpus["kubepods"].String(); got != "1-2" {
		t.Fatalf("primary effective target = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "1-2" {
		t.Fatalf("expected leaf cpuset = %s, want planned target 1-2; writes=%#v", got, cg.writes)
	}
	_ = res
}

func TestComputeEffectiveTargetsDoesNotProtectPodParentOrPodFullCPUSet(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	effective, err := computeEffectiveTargets(dag, false, nil, nil, nil)
	if err != nil {
		t.Fatalf("computeEffectiveTargets: %v", err)
	}
	if got := effective["kubepods"].String(); got != "1-2" {
		t.Fatalf("primary effective target = %s, want desired 1-2 without current leaf widening", got)
	}
}

// TestTopologyCoordinatorConvergeWidensPrimaryEffectiveTargetForPendingAllocation verifies that
// an admit-window container (allocation known, no cgroup leaf yet) folded in via
// PendingProtections also widens the scoped primary effective target, so the parent
// never shrinks below an allocation that is about to materialize.
func TestTopologyCoordinatorConvergeWidensPrimaryEffectiveTargetForPendingAllocation(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2)}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(1, 2, 9)

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		PendingProtections: []PendingProtection{{
			ScopeRel: "kubepods/pod-pending",
			CPUs:     machine.NewCPUSet(9),
			PodUID:   "pod-pending",
		}},
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v", err)
	}
	if got := cg.cpus["kubepods"].String(); got != "1-2,9" {
		t.Fatalf("primary effective target = %s, want 1-2,9 (pending folded in); writes=%#v", got, cg.writes)
	}
	_ = res
}

// TestTopologyCoordinatorConvergeDeductsPrimaryEffectiveCPUsFromReclaim verifies that boundary
// CPUs held by the primary effective target are removed from reclaim targets
// before applying, keeping partitions disjoint without rejecting a recoverable
// transient overlap.
func TestTopologyCoordinatorConvergeConvergesExpectedLeafWithoutDeductingReclaim(t *testing.T) {
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

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			"kubepods/burstable/pod-abc/container-a": machine.NewCPUSet(1, 2),
		},
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubepods"].String(); got != "1-2" {
		t.Fatalf("primary target = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["reclaim"].String(); got != "3-5" {
		t.Fatalf("reclaim target = %s, want final desired 3-5 after witness; writes=%#v", got, cg.writes)
	}
	if !res.Converged {
		t.Fatalf("TopologyCoordinatorConverge did not report final convergence: %+v", res.ConvergenceReport)
	}
}

func TestTopologyCoordinatorConvergePropagatesProtectedRelToPrimaryAndDeductsReclaim(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2)},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(3, 4)},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	effective, err := computeEffectiveTargets(dag, false, nil, nil, nil, map[string]machine.CPUSet{
		"kubepods/podA": machine.NewCPUSet(2, 3),
	})
	if err != nil {
		t.Fatalf("computeEffectiveTargets: %v", err)
	}
	if got := effective["kubepods"].String(); got != "1-3" {
		t.Fatalf("primary effective target = %s, want protected descendant propagated to 1-3", got)
	}
	if got := effective["reclaim"].String(); got != "4" {
		t.Fatalf("reclaim target = %s, want protected CPU 3 deducted", got)
	}
}

func TestTopologyCoordinatorConvergeRejectsReclaimBucketOutsideParentMemsEnvelope(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2), Mems: "0"},
		{Rel: "custom-reclaim-root/reclaimed-1", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(), Mems: "1", Metadata: map[string]string{"numa": "1"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["kubepods"] = machine.NewCPUSet(0)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(1, 2)
	cg.cpus["custom-reclaim-root/reclaimed-1"] = machine.NewCPUSet(1)
	cg.children["custom-reclaim-root"] = []string{"reclaimed-1"}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if !errors.Is(err, ErrInvalidReclaimBucketTarget) {
		t.Fatalf("TopologyCoordinatorConverge error = %v, want %v", err, ErrInvalidReclaimBucketTarget)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("out-of-envelope desired state must fail before writes, got %#v", cg.writes)
	}
}

func TestTopologyCoordinatorConvergeWritesEmptyCPUSetOnCgroupV2(t *testing.T) {
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

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if len(cg.writes) != 1 {
		t.Fatalf("writes = %#v, want one empty cpuset write", cg.writes)
	}
	write := cg.writes[0]
	if write.rel != "kubepods" || write.cpus != "" || !write.writeEmptyCPUs {
		t.Fatalf("v2 empty cpuset write = %#v, want rel=kubepods cpus empty with WriteEmptyCPUs", write)
	}
}

func TestTopologyCoordinatorConvergeAllowsReclaimNUMABucketDisjointReplacementWhenParentContainsTarget(t *testing.T) {
	t.Parallel()

	parentCPUs := machine.NewCPUSet(42, 43, 44, 45, 46, 47, 95, 96, 138, 139, 140, 141, 142, 143, 144, 191)
	numa0CPUs := machine.NewCPUSet(42, 43, 44, 45, 46, 47, 96, 138, 139, 140, 141, 142, 143)
	numa1CPUs := machine.NewCPUSet(95, 144, 191)
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: parentCPUs},
		{Rel: "custom-reclaim-root/numa0", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: numa0CPUs, Metadata: map[string]string{"numa": "0"}},
		{Rel: "custom-reclaim-root/numa1", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: numa1CPUs, Metadata: map[string]string{"numa": "1"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["custom-reclaim-root"] = parentCPUs.Clone()
	cg.cpus["custom-reclaim-root/numa0"] = numa0CPUs.Clone()
	cg.cpus["custom-reclaim-root/numa1"] = numa0CPUs.Clone()
	cg.children["custom-reclaim-root"] = []string{"numa0", "numa1"}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root/numa1"].String(); got != numa1CPUs.String() {
		t.Fatalf("numa1 target = %s, want %s; writes=%#v", got, numa1CPUs.String(), cg.writes)
	}
	if overlap := cg.cpus["custom-reclaim-root/numa0"].Intersection(cg.cpus["custom-reclaim-root/numa1"]); !overlap.IsEmpty() {
		t.Fatalf("reclaim NUMA buckets overlap after apply: overlap=%s writes=%#v", overlap.String(), cg.writes)
	}
}

func TestTopologyCoordinatorConvergeRejectsReclaimNUMABucketDisjointReplacementWithoutReclaimParent(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "custom-reclaim-root/numa1", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(4, 5), Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["custom-reclaim-root/numa1"] = machine.NewCPUSet(1, 2)

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("unparented reclaim NUMA bucket should follow the normal pipeline, got err=%v writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root/numa1"].String(); got != "4-5" {
		t.Fatalf("unparented reclaim NUMA bucket = %s, want 4-5; writes=%#v", got, cg.writes)
	}
}

func TestTopologyCoordinatorConvergeRejectsReclaimNUMABucketSiblingOverlap(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1, 2, 3)},
		{Rel: "custom-reclaim-root/numa0", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(1, 2), Metadata: map[string]string{"numa": "0"}},
		{Rel: "custom-reclaim-root/numa1", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2, 3), Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(1, 2, 3)
	cg.cpus["custom-reclaim-root/numa0"] = machine.NewCPUSet(1, 2)
	cg.cpus["custom-reclaim-root/numa1"] = machine.NewCPUSet(3)

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err == nil || !strings.Contains(err.Error(), "reclaim numa bucket overlap") {
		t.Fatalf("expected reclaim numa bucket overlap error, got err=%v writes=%#v", err, cg.writes)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("overlap validation should fail before writes, got %#v", cg.writes)
	}
}

func TestTopologyCoordinatorConvergeRejectsReclaimNUMABucketOutsideNUMA(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(0, 1)},
		{
			Rel:       "custom-reclaim-root/numa0",
			ParentRel: "custom-reclaim-root",
			Role:      TopoNodeRoleReclaimNUMABucket,
			CPUs:      machine.NewCPUSet(1),
			Metadata:  map[string]string{"numa": "0"},
		},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:    dag,
		Cgroup: cg,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 1},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "outside numa cpuset") {
		t.Fatalf("expected reclaim numa binding error, got err=%v writes=%#v", err, cg.writes)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("numa binding validation should fail before writes, got %#v", cg.writes)
	}
}

func TestTopologyCoordinatorConvergeWidensReclaimParentToContainNUMABucket(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1)},
		{Rel: "custom-reclaim-root/numa0", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(1, 2), Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(1)
	cg.cpus["custom-reclaim-root/numa0"] = machine.NewCPUSet(1)
	cg.children["custom-reclaim-root"] = []string{"numa0"}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root"].String(); got != "1-2" {
		t.Fatalf("reclaim parent target = %s, want 1-2 containing bucket; writes=%#v", got, cg.writes)
	}
}

// TestTopologyCoordinatorConvergeShrinkBlockerCurrentOutsideReason verifies the
// current_outside_parent reason: a child whose cpuset overlaps but is not fully
// inside the new parent target (and has no expected entry) is reported with the
// current_outside_parent reason rather than being mislabeled expected_outside.
func TestTopologyCoordinatorConvergeShrinkBlockerCurrentOutsideReason(t *testing.T) {
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

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err == nil {
		t.Fatalf("expected shrink blocked error, got nil; writes=%#v", cg.writes)
	}
	if !strings.Contains(err.Error(), "apply cpuset.cpus=1 @ primary/pod-y") {
		t.Fatalf("shrink blocker error missing direct child apply failure; got %q", err.Error())
	}
}

func TestTopologyCoordinatorConvergeReportsNonStaleShrinkBlockers(t *testing.T) {
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

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err == nil {
		t.Fatalf("expected non-stale shrink blocker error, got nil; writes=%#v", cg.writes)
	}
	for _, want := range []string{"apply cpuset.cpus=1 @ system/legacy", "forced failure @ system/legacy"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("non-stale shrink blocker error missing %q; got %q", want, err.Error())
		}
	}
}

func TestTopologyCoordinatorConvergeReturnsErrorWhenKubePodLeafConvergeFailsDuringShrink(t *testing.T) {
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

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err == nil {
		t.Fatalf("expected stale pod cgroup converge failure to block shrink; result=%+v writes=%#v", res, cg.writes)
	}
	if !strings.Contains(err.Error(), "apply cpuset.cpus=1 @ kubepods/podabc123") {
		t.Fatalf("error = %v, want direct child apply failure", err)
	}
	if res.Failed == 0 {
		t.Fatalf("result=%+v, want failed child convergence counted", res)
	}
}

func TestTopologyCoordinatorConvergeConvergesStaleResidualWithoutDeductingReclaim(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["kubepods/podabc123"] = machine.NewCPUSet(1, 2)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(2, 3)
	cg.children["kubepods"] = []string{"podabc123"}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubepods"].String(); got != "0-1" {
		t.Fatalf("primary target = %s, want 0-1; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubepods/burstable/pod-abc/container-a"].String(); got != "" {
		t.Fatalf("unmanaged expected leaf cpuset = %s, want unchanged empty; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root"].String(); got != "2-3" {
		t.Fatalf("reclaim target = %s, want unchanged 2-3; writes=%#v", got, cg.writes)
	}
}

func TestTopologyCoordinatorConvergeDoesNotWidenEmptyPrimaryTargetForStaleResidualOnCgroupV2(t *testing.T) {
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

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubepods"].String(); got != "" {
		t.Fatalf("v2 empty primary target = %q, want empty inheritance target; writes=%#v", got, cg.writes)
	}
}

func TestTopologyCoordinatorConvergeConvergesPrimaryAndStaleResidual(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.cpus["kubepods/podabc123"] = machine.NewCPUSet(1, 2)
	cg.children["kubepods"] = []string{"podabc123"}

	if _, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	}); err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["kubepods"].String(); got != "0-1" {
		t.Fatalf("primary target = %s, want 0-1; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["kubepods/podabc123"].String(); got != "0-1" {
		t.Fatalf("planned dynamic pod cpuset = %s, want parent target 0-1; writes=%#v", got, cg.writes)
	}
}

func TestTopologyCoordinatorConvergeConvergesStaleReclaimPodWithoutOverlappingNUMABuckets(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0), Mems: "0"},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1, 2, 3, 4), Mems: "0"},
		{Rel: "custom-reclaim-root/reclaimed-0", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(1, 2), Mems: "0", Metadata: map[string]string{"numa": "0"}},
		{Rel: "custom-reclaim-root/reclaimed-1", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(3, 4), Mems: "0", Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	fullReclaim := machine.NewCPUSet(1, 2, 3, 4)
	cg.cpus["kubepods"] = machine.NewCPUSet(0)
	cg.cpus["custom-reclaim-root"] = fullReclaim.Clone()
	cg.cpus["custom-reclaim-root/reclaimed-0"] = fullReclaim.Clone()
	cg.cpus["custom-reclaim-root/reclaimed-0/pod-stale-a"] = fullReclaim.Clone()
	cg.cpus["custom-reclaim-root/reclaimed-1"] = fullReclaim.Clone()
	cg.cpus["custom-reclaim-root/reclaimed-1/pod-stale-b"] = fullReclaim.Clone()
	cg.children["custom-reclaim-root"] = []string{"reclaimed-0", "reclaimed-1"}
	cg.children["custom-reclaim-root/reclaimed-0"] = []string{"pod-stale-a"}
	cg.children["custom-reclaim-root/reclaimed-1"] = []string{"pod-stale-b"}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err != nil {
		t.Fatalf("TopologyCoordinatorConverge: %v; writes=%#v", err, cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root/reclaimed-0"].String(); got != "1-2" {
		t.Fatalf("reclaimed-0 = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root/reclaimed-0/pod-stale-a"].String(); got != "1-2" {
		t.Fatalf("stale pod a = %s, want 1-2; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root/reclaimed-1"].String(); got != "3-4" {
		t.Fatalf("reclaimed-1 = %s, want 3-4; writes=%#v", got, cg.writes)
	}
	if got := cg.cpus["custom-reclaim-root/reclaimed-1/pod-stale-b"].String(); got != "3-4" {
		t.Fatalf("stale pod b = %s, want 3-4; writes=%#v", got, cg.writes)
	}
	if overlap := cg.cpus["custom-reclaim-root/reclaimed-0"].Intersection(cg.cpus["custom-reclaim-root/reclaimed-1"]); !overlap.IsEmpty() {
		t.Fatalf("reclaim NUMA buckets overlap: %s; writes=%#v", overlap.String(), cg.writes)
	}
}

func TestTopologyCoordinatorConvergeReturnsErrorWhenStaleReclaimPodConvergeFails(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3), Mems: "0"},
		{Rel: "custom-reclaim-root/reclaimed-0", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2), Mems: "0", Metadata: map[string]string{"numa": "0"}},
		{Rel: "custom-reclaim-root/reclaimed-1", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(3), Mems: "0", Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(2, 3, 4)
	cg.cpus["custom-reclaim-root/reclaimed-0"] = machine.NewCPUSet(2)
	cg.cpus["custom-reclaim-root/reclaimed-1"] = machine.NewCPUSet(3, 4)
	cg.cpus["custom-reclaim-root/reclaimed-1/pod-stale"] = machine.NewCPUSet(4)
	cg.children["custom-reclaim-root"] = []string{"reclaimed-0", "reclaimed-1"}
	cg.children["custom-reclaim-root/reclaimed-1"] = []string{"pod-stale"}
	cg.failRel["custom-reclaim-root/reclaimed-1/pod-stale"] = true

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err == nil {
		t.Fatalf("expected stale reclaim pod converge failure; result=%+v writes=%#v", res, cg.writes)
	}
	if !strings.Contains(err.Error(), "custom-reclaim-root/reclaimed-1/pod-stale") {
		t.Fatalf("error = %v, want direct stale child apply failure", err)
	}
}

// TestTopologyCoordinatorConvergeRejectsReclaimBucketShrinkWithoutProvenDescendant
// pins the ownership contract: a reclaim NUMA bucket that shrinks to a target which
// no longer contains its unproven dynamic pod child must fail closed. The reset
// never propagates the parent target to an unproven descendant, so it must not
// silently strand the child; it errors out and never writes the pod.
func TestTopologyCoordinatorConvergeRejectsReclaimBucketShrinkWithoutProvenDescendant(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(1, 2, 3, 25, 26, 27), Mems: "0-1"},
		{Rel: "custom-reclaim-root/reclaimed-1", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(25, 26, 27), Mems: "1", Metadata: map[string]string{"numa": "1"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.enforceParentContainsTarget = true
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(1, 2, 3, 25, 26, 27)
	cg.cpus["custom-reclaim-root/reclaimed-1"] = machine.NewCPUSet(25, 26, 27)
	cg.cpus["custom-reclaim-root/reclaimed-1/pod022"] = machine.NewCPUSet(0, 1, 2, 3)
	cg.children["custom-reclaim-root"] = []string{"reclaimed-1"}
	cg.children["custom-reclaim-root/reclaimed-1"] = []string{"pod022"}
	cg.onApply = func(rel string, data *cgcommon.CPUSetData) {
		cpus, err := machine.Parse(data.CPUs)
		if err == nil {
			cg.cpus[rel] = cpus
		}
	}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:    dag,
		Cgroup: cg,
		Mems:   "0-1",
		Mode:   ResetModeGuard(),
	})
	// The reclaim bucket target (25-27) no longer contains the unproven pod
	// child (0-3); without descendant propagation the reset must fail closed rather
	// than clamp the pod.
	if err == nil {
		t.Fatalf("expected fail-closed containment error, writes=%#v", cg.writes)
	}
	for _, write := range cg.writes {
		if write.rel == "custom-reclaim-root/reclaimed-1/pod022" {
			t.Fatalf("unproven reclaim pod must never be written; got=%#v", write)
		}
	}
}

func TestTopologyCoordinatorConvergeReturnsErrorWhenStaleReclaimPodConvergeFailsAfterPrimaryDeduction(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "kubepods", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1, 4), Mems: "0"},
		{Rel: "custom-reclaim-root", Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(2, 3, 4), Mems: "0"},
		{Rel: "custom-reclaim-root/reclaimed-0", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(2), Mems: "0", Metadata: map[string]string{"numa": "0"}},
		{Rel: "custom-reclaim-root/reclaimed-1", ParentRel: "custom-reclaim-root", Role: TopoNodeRoleReclaimNUMABucket, CPUs: machine.NewCPUSet(3, 4), Mems: "0", Metadata: map[string]string{"numa": "0"}},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["kubepods"] = machine.NewCPUSet(0, 1, 4)
	cg.cpus["custom-reclaim-root"] = machine.NewCPUSet(2, 3, 4)
	cg.cpus["custom-reclaim-root/reclaimed-0"] = machine.NewCPUSet(2)
	cg.cpus["custom-reclaim-root/reclaimed-1"] = machine.NewCPUSet(3, 4)
	cg.cpus["custom-reclaim-root/reclaimed-1/pod-stale"] = machine.NewCPUSet(4)
	cg.children["custom-reclaim-root"] = []string{"reclaimed-0", "reclaimed-1"}
	cg.children["custom-reclaim-root/reclaimed-1"] = []string{"pod-stale"}
	cg.failRel["custom-reclaim-root/reclaimed-1/pod-stale"] = true

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err == nil {
		t.Fatalf("expected stale reclaim pod converge failure after primary deduction; result=%+v writes=%#v", res, cg.writes)
	}
	if !strings.Contains(err.Error(), "custom-reclaim-root/reclaimed-1/pod-stale") {
		t.Fatalf("error = %v, want direct stale child apply failure", err)
	}
}

// TestTopologyCoordinatorConvergeWriteAndDescendStopsOnApplyFailure verifies that when
// expandDescendants fails to apply a cpuset at some intermediate rel, it does
// not continue writing further descendants under that failed parent. Otherwise
// TestTopologyCoordinatorConvergeWriteAndDescendSurfacesApplyFailure verifies that a
// descendant convergence failure is reported to the caller.
func TestTopologyCoordinatorConvergeWriteAndDescendSurfacesApplyFailure(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"burstable"}
	cg.children["primary/burstable"] = []string{"leaf"}
	// The middle intermediate is an explicit operation and its failure aborts
	// execution without plan-external descent.
	cg.failRel["primary/burstable"] = true

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err == nil {
		t.Fatalf("planned dynamic intermediate failure was ignored; writes=%#v", cg.writes)
	}
}

func TestTopologyCoordinatorConvergeFailsClosedWhenPlannedDynamicChildDisappears(t *testing.T) {
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

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err == nil {
		t.Fatalf("TopologyCoordinatorConverge unexpectedly ignored disappeared planned child; result=%+v writes=%#v", res, cg.writes)
	}
	if res.Failed != 1 || res.Skipped != 0 {
		t.Fatalf("result = %+v, want one failed planned write and no skip", res)
	}
}

func TestTopologyCoordinatorConvergeFailsClosedWhenPlannedDynamicIntermediateDisappears(t *testing.T) {
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

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err == nil {
		t.Fatalf("TopologyCoordinatorConverge unexpectedly ignored disappeared planned intermediate; result=%+v writes=%#v", res, cg.writes)
	}
	if res.Failed != 1 || res.Skipped != 0 {
		t.Fatalf("result = %+v, want one failed planned write and no skip", res)
	}
}

func TestTopologyCoordinatorConvergeFailsClosedWhenPlannedExpectedLeafDisappears(t *testing.T) {
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

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
		ExpectedCPUSetByRel: map[string]machine.CPUSet{
			expectedRel: machine.NewCPUSet(1),
		},
	})
	if err == nil {
		t.Fatalf("TopologyCoordinatorConverge unexpectedly ignored disappeared expected leaf; result=%+v writes=%#v", res, cg.writes)
	}
	if res.Failed != 1 || res.Skipped != 0 {
		t.Fatalf("result = %+v, want one failed planned write and no skip", res)
	}
}

func TestTopologyCoordinatorConvergeReturnsNonNotFoundDynamicChildError(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.children["primary"] = []string{"child-a"}
	cg.applyErr["primary/child-a"] = syscall.EINVAL

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
	})
	if err == nil {
		t.Fatalf("planned dynamic child failure was ignored; result=%+v writes=%#v", res, cg.writes)
	}
	if res.Failed != 1 {
		t.Fatalf("result = %+v, want one failed planned write", res)
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
		fmt.Errorf("write cpuset.cpus: no such device"),
	} {
		if !isCgroupNotFoundError(err) {
			t.Fatalf("isCgroupNotFoundError(%v) = false, want true", err)
		}
	}
	if isCgroupNotFoundError(syscall.EINVAL) {
		t.Fatalf("isCgroupNotFoundError(EINVAL) = true, want false")
	}
}

// TestTopologyCoordinatorConvergeExpandStopsOnNodeGrowFailure verifies that when a controlled
// node's own grow write fails, TopologyCoordinatorConverge does not descend into its subtree.
// Otherwise descendants would be written to the (larger) effective target while
// the node itself is still at the smaller observed cpuset, violating the cgroup
// v1 parent-superset invariant.
func TestTopologyCoordinatorConvergeExpandStopsOnNodeGrowFailure(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0"}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	// primary is empty -> grow to {0,1}; its own write fails.
	cg.children["primary"] = []string{"leaf"}
	cg.failRel["primary"] = true

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: testCPUDetails(),
		Mems:       "0",
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
