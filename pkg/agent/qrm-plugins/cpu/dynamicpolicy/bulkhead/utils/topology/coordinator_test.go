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
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils/topology/deadlock"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	cgcommon "github.com/kubewharf/katalyst-core/pkg/util/cgroup/common"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

type coordinatorSnapshotTestCgroup struct {
	*topologyFakeCgroup
	driver HierarchyDriver
}

type publicationReplanMarkerError struct {
	err error
}

func (e *publicationReplanMarkerError) Error() string { return e.err.Error() }
func (e *publicationReplanMarkerError) Unwrap() error { return e.err }
func (*publicationReplanMarkerError) ReplanRequired() bool {
	return true
}

type coordinatorVersionOnlyCgroup struct {
	cgroupclient.FakeCgroupClient
	version cgroupclient.CgroupVersion
}

func (c *coordinatorVersionOnlyCgroup) Version(context.Context) cgroupclient.CgroupVersion {
	return c.version
}

func (c *coordinatorSnapshotTestCgroup) SnapshotDriver() HierarchyDriver {
	return c.driver
}

func TestCoordinatorHierarchyDriverSelectsVersionPolicyAndPreservesSnapshotHook(t *testing.T) {
	t.Parallel()
	root := resolvedPath(t, t.TempDir())
	for _, version := range []cgroupclient.CgroupVersion{
		cgroupclient.CgroupVersionV1,
		cgroupclient.CgroupVersionV2,
	} {
		driver, err := newCoordinatorHierarchyDriver(
			context.Background(),
			&coordinatorVersionOnlyCgroup{version: version},
			root,
		)
		if err != nil {
			t.Fatalf("version %v constructor error = %v", version, err)
		}
		concrete, ok := driver.(*cgroupFSDriver)
		if !ok {
			t.Fatalf("version %v driver type = %T, want *cgroupFSDriver", version, driver)
		}
		wantPolicy := cgroupV1Policy
		if version == cgroupclient.CgroupVersionV2 {
			wantPolicy = cgroupV2Policy
		}
		if concrete.policy != wantPolicy {
			t.Fatalf("version %v policy = %v, want %v", version, concrete.policy, wantPolicy)
		}
		_ = driver.Close()
	}

	hook := newFakeHierarchyDriver()
	cg := newTopologyFakeCgroup()
	provider := &coordinatorSnapshotTestCgroup{topologyFakeCgroup: cg, driver: hook}
	got, err := snapshotDriverForCoordinator(context.Background(), provider)
	if err != nil {
		t.Fatalf("snapshotDriverForCoordinator(hook) error = %v", err)
	}
	if got != hook {
		t.Fatalf("snapshotDriverForCoordinator(hook) = %T/%p, want supplied hook %p", got, got, hook)
	}
}

func TestCoordinatorRoundAdmissionSafetyCPUSetOnlyCoversPendingAllocation(t *testing.T) {
	t.Parallel()
	round := &coordinatorRound{
		protectedPending: machine.NewCPUSet(1),
		dynamicByRel: map[string]machine.CPUSet{
			"primary/existing/container": machine.NewCPUSet(2),
		},
		deferredByRel: map[string]machine.CPUSet{
			"primary/deferred/container": machine.NewCPUSet(3),
		},
	}

	got := round.admissionSafetyCPUSet()
	if !got.Equals(machine.NewCPUSet(1)) {
		t.Fatalf("admissionSafetyCPUSet() = %s, want only pending allocation CPU 1", got.String())
	}
}

func TestParentSafeAdmissionCompilesReservesAndExecutesOneFrozenTrace(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9ParentSafeFixture(t)
	result := &ConvergenceResult{}
	published := 0
	expectedTrace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err)

	outcome, err := fixture.round.executeParentSafeAdmission(
		context.Background(), base, result,
		func(*CompleteSnapshot) error {
			published++
			return nil
		},
		func(*CompleteSnapshot, map[string]struct{}) error {
			published++
			return nil
		},
	)
	if err != nil {
		t.Fatalf("executeParentSafeAdmission() error = %v", err)
	}
	if len(outcome.Journal) != len(flattenTraceOperations(expectedTrace)) {
		t.Fatalf("executed journal = %d operations, want frozen trace length %d",
			len(outcome.Journal), len(flattenTraceOperations(expectedTrace)))
	}
	if published != 1 || (!result.ParentSafe && !result.Converged) || !result.FinalSnapshotCurrent ||
		!result.Published || result.ReplanDisposition != ReplanNotAllowed {
		t.Fatalf("published=%d result=%+v, want one successful frozen-trace publication", published, result)
	}
}

func TestParentSafeAdmissionPublishStalePreservesActualWritesAndFinalSnapshot(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9ParentSafeFixture(t)
	adjustmentBudget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 100,
	})
	fixture.round.adjustmentBudget = adjustmentBudget
	initial := fixture.driver.snapshot()
	result := &ConvergenceResult{}
	publishCalls := 0
	publishErr := &PlanStaleError{
		Rel:       "primary/container",
		Direction: WritePublish,
		Resource:  "container_cpuset",
		Current:   "0",
		Target:    "1",
	}

	outcome, err := fixture.round.executeParentSafeAdmission(
		context.Background(), base, result, nil,
		func(snapshot *CompleteSnapshot, _ map[string]struct{}) error {
			publishCalls++
			require.NotNil(t, snapshot)
			return publishErr
		},
	)

	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.Equal(t, 1, publishCalls, "stale publication must be handed directly to the caller")
	require.True(t, result.ParentSafe)
	require.False(t, result.Converged)
	require.True(t, result.FinalSnapshotCurrent)
	require.NotNil(t, result.FinalSnapshot)
	require.False(t, result.Published)
	require.Equal(t, ReplanSafeFromVerifiedFinalState, result.ReplanDisposition)
	require.NotEmpty(t, result.Journal)
	require.Positive(t, result.Applied)
	require.NotEqual(t, initial, fixture.driver.snapshot(),
		"publication stale is not an execution failure and must not roll back actual writes")
	require.Equal(t, result.FinalSnapshot, outcome.Snapshot)
	require.True(t, adjustmentBudget.ReplanSafe(result.ReplanDisposition),
		"fresh final scan after real frozen writes must authorize the verified-final-state replan")
}

func TestAdmissionCompileFailurePerformsZeroPhysicalWrites(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9ParentSafeFixture(t)
	fixture.round.maxRounds = 0

	_, err := fixture.round.executeParentSafeAdmission(
		context.Background(), base, &ConvergenceResult{}, nil, nil)
	if !errors.Is(err, ErrRoundBudgetExceeded) {
		t.Fatalf("executeParentSafeAdmission() error = %v, want %v", err, ErrRoundBudgetExceeded)
	}
	if fixture.driver.PhysicalWriteCount() != 0 {
		t.Fatalf("compile failure performed %d physical writes", fixture.driver.PhysicalWriteCount())
	}
}

func TestAdmissionReservationFailurePerformsZeroPhysicalWrites(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9ParentSafeFixture(t)
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	if err != nil {
		t.Fatalf("compileFixedPointTrace() error = %v", err)
	}
	fixture.round.round = 0
	fixture.round.admissionBudget = &AdmissionConvergenceBudget{
		MaxRequiredWrites: trace.Cost.Forward.Total() - 1,
	}

	_, err = fixture.round.executeParentSafeAdmission(
		context.Background(), base, &ConvergenceResult{}, nil, nil)
	if !errors.Is(err, ErrAdmissionReservationExceeded) {
		t.Fatalf("executeParentSafeAdmission() error = %v, want %v", err, ErrAdmissionReservationExceeded)
	}
	if fixture.driver.PhysicalWriteCount() != 0 {
		t.Fatalf("reservation failure performed %d physical writes", fixture.driver.PhysicalWriteCount())
	}
}

func TestAdmissionPreflightDriftPerformsZeroPhysicalWrites(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9ParentSafeFixture(t)
	drifted := false
	fixture.driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationStat && rel == "kubepods" && !drifted {
			drifted = true
			fixture.driver.bumpIdentity("kubepods")
		}
		return nil
	}

	_, err := fixture.round.executeParentSafeAdmission(
		context.Background(), base, &ConvergenceResult{}, nil, nil)
	if err == nil {
		t.Fatal("executeParentSafeAdmission() error = nil, want preflight drift")
	}
	if fixture.driver.PhysicalWriteCount() != 0 {
		t.Fatalf("preflight drift performed %d physical writes", fixture.driver.PhysicalWriteCount())
	}
}

func TestAdmissionPostWriteDriftRollsBackBeforeReturning(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9ParentSafeFixture(t)
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	if err != nil {
		t.Fatalf("compileFixedPointTrace() error = %v", err)
	}
	fixture.round.round = 0
	initial := fixture.driver.snapshot()
	injected := &injectedTraceDriver{
		HierarchyDriver: fixture.driver,
		injection:       traceFailureInjection{failFinalProof: true},
		expectedForward: len(expectedPhysicalWrites(trace)),
	}
	fixture.round.driver = injected

	_, err = fixture.round.executeParentSafeAdmission(
		context.Background(), base, &ConvergenceResult{}, nil, nil)
	if err == nil {
		t.Fatal("executeParentSafeAdmission() error = nil, want post-write drift")
	}
	if !injected.injected {
		t.Fatal("post-write failure injection was not reached")
	}
	if got := fixture.driver.snapshot(); !reflect.DeepEqual(got, initial) {
		t.Fatalf("post-write drift did not roll back complete prefix: got=%#v want=%#v", got, initial)
	}
}

func TestAdmissionPreservesVerifiedRollbackReplanDisposition(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9ParentSafeFixture(t)
	adjustmentBudget := NewAdjustmentBudget(context.Background(), ConvergenceBudget{
		MaxPlanOperations: 100,
	})
	fixture.round.adjustmentBudget = adjustmentBudget
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err)
	fixture.round.round = 0
	operations := flattenTraceOperations(trace)
	require.GreaterOrEqual(t, len(operations), 2)
	nextRel := ""
	for _, operation := range operations[1:] {
		if operation.Rel != operations[0].Rel {
			nextRel = operation.Rel
			break
		}
	}
	require.NotEmpty(t, nextRel)
	initial := fixture.driver.snapshot()
	fixture.driver.invariants = nil
	driver := newPreWriteDriftDriver(fixture.driver, trace.InitialSnapshot, 1, nextRel)
	fixture.round.driver = driver
	result := &ConvergenceResult{}

	_, err = fixture.round.executeParentSafeAdmission(
		context.Background(), base, result, nil, nil)

	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.Equal(t, ReplanSafeAfterVerifiedRollback, result.ReplanDisposition)
	require.Positive(t, result.forwardWriteAttempts)
	require.Positive(t, result.rollbackWriteAttempts)
	require.True(t, adjustmentBudget.ReplanSafe(result.ReplanDisposition),
		"complete rollback verification after real frozen writes must authorize replan")
	driftedState := fixture.driver.snapshot()
	delete(initial, driver.driftedRel)
	delete(driftedState, driver.driftedRel)
	require.Equal(t, initial, driftedState)
}

func TestAdmissionPublishesOnlyAfterFreshFinalParentSafeProof(t *testing.T) {
	t.Parallel()
	fixture, base := newTask9ParentSafeFixture(t)
	result := &ConvergenceResult{}
	published := false
	expectedTrace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err)
	publish := func(snapshot *CompleteSnapshot) error {
		if snapshot == nil || snapshot.ID != expectedTrace.FinalSnapshot.ID {
			return errors.New("publication did not receive the fresh final proof")
		}
		if result.FinalSnapshotCurrent || result.FinalSnapshot != nil ||
			result.ParentSafe || result.Converged {
			return errors.New("result committed before publication succeeded")
		}
		published = true
		return nil
	}

	_, err = fixture.round.executeParentSafeAdmission(
		context.Background(), base, result, publish,
		func(snapshot *CompleteSnapshot, _ map[string]struct{}) error {
			return publish(snapshot)
		},
	)
	if err != nil {
		t.Fatalf("executeParentSafeAdmission() error = %v", err)
	}
	if !published {
		t.Fatal("fresh final ParentSafe proof was not published")
	}
	if !result.FinalSnapshotCurrent || result.FinalSnapshot == nil ||
		(!result.ParentSafe && !result.Converged) {
		t.Fatalf("successful publication did not commit result: %+v", result)
	}
}

func TestAdmissionFinalizeFailureRollsBackFrozenTrace(t *testing.T) {
	t.Parallel()
	publishParentSafeErr := errors.New("injected PublishParentSafe failure")
	publishFinalErr := errors.New("injected PublishFinal failure")
	tests := []struct {
		name              string
		deferred          bool
		wantErr           error
		publishFinal      func(context.CancelFunc) func(*CompleteSnapshot) error
		publishParentSafe func(context.CancelFunc) func(*CompleteSnapshot, map[string]struct{}) error
	}{
		{
			name:    "proof后cancel",
			wantErr: context.Canceled,
			publishFinal: func(cancel context.CancelFunc) func(*CompleteSnapshot) error {
				return func(*CompleteSnapshot) error {
					cancel()
					return context.Canceled
				}
			},
		},
		{
			name:     "PublishParentSafe失败",
			deferred: true,
			wantErr:  publishParentSafeErr,
			publishParentSafe: func(context.CancelFunc) func(*CompleteSnapshot, map[string]struct{}) error {
				return func(*CompleteSnapshot, map[string]struct{}) error {
					return publishParentSafeErr
				}
			},
		},
		{
			name:    "PublishFinal失败",
			wantErr: publishFinalErr,
			publishFinal: func(context.CancelFunc) func(*CompleteSnapshot) error {
				return func(*CompleteSnapshot) error {
					return publishFinalErr
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fixture, base := newTask9FinalizeFixture(t, tt.deferred)
			initial := fixture.driver.snapshot()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var publishFinal func(*CompleteSnapshot) error
			if tt.publishFinal != nil {
				publishFinal = tt.publishFinal(cancel)
			}
			var publishParentSafe func(*CompleteSnapshot, map[string]struct{}) error
			if tt.publishParentSafe != nil {
				publishParentSafe = tt.publishParentSafe(cancel)
			}
			result := &ConvergenceResult{}

			outcome, err := fixture.round.executeParentSafeAdmission(
				ctx, base, result, publishFinal, publishParentSafe)

			require.ErrorIs(t, err, tt.wantErr)
			require.Equal(t, initial, fixture.driver.snapshot())
			require.False(t, result.ParentSafe)
			require.False(t, result.Converged)
			require.False(t, result.FinalSnapshotCurrent)
			require.Nil(t, result.FinalSnapshot)
			require.Empty(t, result.Journal)
			require.Zero(t, result.Applied)
			require.False(t, result.Published)
			require.Equal(t, ReplanNotAllowed, result.ReplanDisposition)
			require.Equal(t, ConvergenceStateNonConverged, result.State)
			require.Equal(t, RoundStatusBlocked, outcome.Status)
			require.Positive(t, fixture.driver.PhysicalWriteCount(),
				"failed finalization must execute and roll back a physical write prefix")
			require.Positive(t, result.forwardWriteAttempts)
			require.Positive(t, result.rollbackWriteAttempts)
		})
	}
}

func TestDeferredCleanupRemainsOutsideParentSafeTrace(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	fixture.configureProtectedDeferredEvaluationInputs()
	base := fixture.snapshot()
	fixture.round.admissionBudget = &AdmissionConvergenceBudget{MaxRequiredWrites: 100}
	var publishedDeferred map[string]struct{}
	expectedTrace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err)

	_, err = fixture.round.executeParentSafeAdmission(
		context.Background(), base, &ConvergenceResult{}, nil,
		func(_ *CompleteSnapshot, deferred map[string]struct{}) error {
			publishedDeferred = cloneRelSet(deferred)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("executeParentSafeAdmission() error = %v", err)
	}
	for _, operation := range flattenTraceOperations(expectedTrace) {
		if _, deferred := expectedTrace.EvaluationInput.DeferredCleanupRels[operation.Rel]; deferred {
			t.Fatalf("deferred cleanup rel %q leaked into frozen ParentSafe trace", operation.Rel)
		}
	}
	if !reflect.DeepEqual(publishedDeferred, expectedTrace.EvaluationInput.DeferredCleanupRels) {
		t.Fatalf("published deferred cleanup rels = %#v, want %#v",
			publishedDeferred, expectedTrace.EvaluationInput.DeferredCleanupRels)
	}
}

func newTask9ParentSafeFixture(t *testing.T) (*admissionTraceFixture, *CompleteSnapshot) {
	return newTask9FinalizeFixture(t, true)
}

func newTask9FinalizeFixture(
	t *testing.T,
	deferred bool,
) (*admissionTraceFixture, *CompleteSnapshot) {
	t.Helper()
	fixture := newAdmissionTraceFixture(t)
	if deferred {
		fixture.configureStagedSMTTransferWithDynamicDescendant()
		fixture.configureProtectedDeferredEvaluationInputs()
		fixture.driver.nodes["reclaimed/leaf"].configuredCPUs = machine.NewCPUSet()
		fixture.driver.nodes["reclaimed/leaf"].cpus = machine.NewCPUSet(4)
	} else {
		fixture.driver.capabilities = cgroupV2Policy.capabilities(true)
		fixture.round.allowEmptyTarget = true
		fixture.cpuDetails[4] = machine.CPUTopoInfo{NUMANodeID: 0}
		fixture.addPrimary("kubepods", "1-3", "0")
		fixture.addReclaim("reclaimed", "0,4", "0")
		fixture.requireCPUSet("kubepods", "0-3")
		fixture.dynamicByRel["kubepods"] = machine.MustParse("0-3")
		fixture.targetByRel["reclaimed"] = machine.NewCPUSet(4)
	}
	base := fixture.snapshot()
	fixture.round.admissionBudget = &AdmissionConvergenceBudget{MaxRequiredWrites: 100}
	return fixture, base
}

func TestTopologyCoordinatorV1FourNUMADormantBucketsConvergeWithPhysicalParentEnvelope(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.MustParse("0-3"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.MustParse("4-7"), Mems: "0-3", TrustAnchor: true},
		{Rel: "reclaim/numa-0", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "0", Metadata: map[string]string{"numa": "0"}},
		{Rel: "reclaim/numa-1", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.NewCPUSet(), Mems: "1", Metadata: map[string]string{"numa": "1"}},
		{Rel: "reclaim/numa-2", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("4-5"), Mems: "2", Metadata: map[string]string{"numa": "2"}},
		{Rel: "reclaim/numa-3", ParentRel: "reclaim", Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim, CPUs: machine.MustParse("6-7"), Mems: "3", Metadata: map[string]string{"numa": "3"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	driver := newFakeHierarchyDriver()
	driver.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0-3")
	driver.add("reclaim", CgroupIdentity{Device: 1, Inode: 2}, "4-7", "0-3")
	driver.add("reclaim/numa-0", CgroupIdentity{Device: 1, Inode: 3}, "0-7", "0")
	driver.add("reclaim/numa-1", CgroupIdentity{Device: 1, Inode: 4}, "0-7", "1")
	driver.add("reclaim/numa-2", CgroupIdentity{Device: 1, Inode: 5}, "4-5", "2")
	driver.add("reclaim/numa-3", CgroupIdentity{Device: 1, Inode: 6}, "6-7", "3")
	driver.markInactive("reclaim/numa-0")
	driver.markInactive("reclaim/numa-1")
	driver.witnessAuthorizedExpansions = map[string]machine.CPUSet{
		"reclaim": machine.MustParse("0-3"),
	}
	dormantReads := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && (rel == "reclaim/numa-0" || rel == "reclaim/numa-1") {
			dormantReads++
		}
		return nil
	}
	cg := newTopologyFakeCgroup()
	cg.version = cgroupclient.CgroupVersionV1
	provider := &coordinatorSnapshotTestCgroup{topologyFakeCgroup: cg, driver: driver}
	var published *CompleteSnapshot

	result, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: provider, Mems: "0-3",
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0}, 1: {NUMANodeID: 0},
			2: {NUMANodeID: 1}, 3: {NUMANodeID: 1},
			4: {NUMANodeID: 2}, 5: {NUMANodeID: 2},
			6: {NUMANodeID: 3}, 7: {NUMANodeID: 3},
		},
		PublishFinalSnapshot: func(snapshot *CompleteSnapshot) error {
			published = snapshot
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Converge() error = %v", err)
	}
	if !result.Converged || !result.FinalSnapshotCurrent {
		t.Fatalf("result = %+v, want current converged proof", result)
	}
	writesByRel := make(map[string]int)
	for _, write := range driver.writes {
		writesByRel[write.rel]++
	}
	if writesByRel["reclaim"] == 0 {
		t.Fatalf("writer mutations = %v, want physical parent envelope write", driver.writes)
	}
	for _, rel := range []string{"reclaim/numa-0", "reclaim/numa-1"} {
		if writesByRel[rel] != 0 {
			t.Fatalf("dormant rel %q writes = %d, want no reclaim ownership write", rel, writesByRel[rel])
		}
	}
	if dormantReads < 8 {
		t.Fatalf("dormant activity reads = %d, want both buckets revalidated across round and final proofs", dormantReads)
	}
	// Semantic reclaim ownership lives in OwnershipByRel; DomainUnion stays the
	// closed physical evidence union (the dormant v1 envelopes remain observed).
	if got, want := result.FinalSnapshot.OwnershipByRel["reclaim"], machine.MustParse("4-7"); !got.Equals(want) {
		t.Fatalf("published reclaim semantic ownership = %s, want %s", got.String(), want.String())
	}
	if got, want := result.FinalSnapshot.DomainUnion[DomainReclaim], machine.MustParse("0-7"); !got.Equals(want) {
		t.Fatalf("published reclaim physical evidence union = %s, want observed entries %s", got.String(), want.String())
	}
	if published == nil || published == result.FinalSnapshot {
		t.Fatalf("published snapshot = %p, final snapshot = %p, want independent copies",
			published, result.FinalSnapshot)
	}
	if published.ID != result.FinalSnapshot.ID {
		t.Fatalf("published snapshot ID = %x, final snapshot ID = %x, want identical evidence",
			published.ID, result.FinalSnapshot.ID)
	}
	for _, rel := range []string{"reclaim/numa-0", "reclaim/numa-1"} {
		if got, ok := published.TargetProofCPUs(rel, machine.NewCPUSet()); !ok || !got.IsEmpty() {
			t.Fatalf("published dormant ownership %q = %s, found=%v, want empty semantic proof", rel, got.String(), ok)
		}
		if published.Entries[rel].CPUs.IsEmpty() {
			t.Fatalf("published physical observation %q unexpectedly empty", rel)
		}
	}
}

func TestTopologyCoordinatorAdmissionDeadlineCoversFreshProofAndFailsClosed(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	ctx := newTriggeredDeadlineContext(context.Background())
	primaryScans := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op != HierarchyOperationStat || rel != "primary" {
			return nil
		}
		primaryScans++
		// A no-op converged round takes three snapshots before the independent
		// fresh publish proof. Expire exactly at that proof boundary so the test
		// is independent of scheduler speed and race/coverage overhead.
		if primaryScans == 4 {
			ctx.expire()
		}
		return nil
	}
	published := false

	result, err := (TopologyCoordinator{}).Converge(ctx, CoordinatorInput{
		DAG: dag, Cgroup: cg, Objective: ConvergenceObjectiveParentSafe,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0},
		},
		PublishFinalSnapshot: func(*CompleteSnapshot) error {
			published = true
			return nil
		},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Converge() error = %v, want admission deadline exceeded", err)
	}
	if result.ParentSafe || result.Converged || result.FinalSnapshotCurrent || result.FinalSnapshot != nil {
		t.Fatalf("timed-out result = %+v, want fail-closed without parent-safe/final proof", result)
	}
	if published {
		t.Fatal("timed-out fresh proof must not publish a final snapshot")
	}
}

func TestTopologyCoordinatorPreservesPublishedSuccessfulProofWhenContextExpiresAtReturn(t *testing.T) {
	t.Parallel()
	dag, cg, _ := newCoordinatorSnapshotTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	result, err := (TopologyCoordinator{}).Converge(ctx, CoordinatorInput{
		DAG: dag, Cgroup: cg, Objective: ConvergenceObjectiveParentSafe,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0},
		},
		PublishFinalSnapshot: func(*CompleteSnapshot) error {
			cancel()
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Converge() error = %v, want already-published proof preserved", err)
	}
	if (!result.ParentSafe && !result.Converged) || !result.FinalSnapshotCurrent || result.FinalSnapshot == nil {
		t.Fatalf("result = %+v, want published successful final proof preserved", result)
	}
}

func TestTopologyCoordinatorV2DisabledResetConvergesOnEmptyConfiguredCPUs(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(), Mems: "0", TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	base := newFakeHierarchyDriver()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	base.add("primary", identity, "0-3", "0")
	driver := &resetConvergenceStateDriver{
		HierarchyDriver: base,
		states: map[string]EntryState{"primary": {
			Rel: "primary", Identity: identity,
			CPUs: machine.MustParse("0-3"), ConfiguredCPUs: machine.MustParse("1-2"),
			Mems: "0", ConfiguredMems: "0",
		}},
		capabilities: cgroupV2Policy.capabilities(true),
	}
	cg := newTopologyFakeCgroup()
	cg.version = cgroupclient.CgroupVersionV2
	provider := &coordinatorSnapshotTestCgroup{topologyFakeCgroup: cg, driver: driver}

	result, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: provider, Mems: "0", Mode: ResetModeGuard(),
	})
	if err != nil {
		t.Fatalf("Converge(reset) error = %v", err)
	}
	if !result.Converged || result.State != ConvergenceStateConverged {
		t.Fatalf("result = %+v, want converged v2 empty reset", result)
	}
	if !result.Published || result.ReplanDisposition != ReplanNotAllowed {
		t.Fatalf("result = %+v, successful reset must explicitly report published terminal state", result)
	}
	state := driver.states["primary"]
	got := state.CPUs.String()
	if !state.ConfiguredCPUs.IsEmpty() || got != "0-3" {
		t.Fatalf("state configured/effective = %q/%q, want empty/0-3", state.ConfiguredCPUs.String(), got)
	}
}

func TestTopologyCoordinatorResetFirstWriterReadStaleAuthorizesNoPhysicalWriteReplan(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "missing entry", err: syscall.ENOENT},
		{name: "identity changed", err: ErrCgroupIdentityChanged},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
			driver.nodes["primary"].cpus = machine.MustParse("0-1")
			driver.nodes["primary"].configuredCPUs = machine.MustParse("0-1")
			readCalls := 0
			driver.beforeCall = func(op HierarchyOperation, rel string) error {
				if op == HierarchyOperationRead && rel == "primary" {
					readCalls++
					if readCalls == 1 {
						return tt.err
					}
				}
				return nil
			}

			res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
				DAG: dag, Cgroup: cg, Mems: "0", Mode: ResetModeGuard(),
				Budget: ConvergenceBudget{MaxHierarchyIOOperations: 1000},
			})

			require.ErrorIs(t, err, tt.err, "readCalls=%d result=%+v", readCalls, res)
			require.Equal(t, 1, readCalls, "the injected stale read must be the reset writer's first ReadEntry")
			require.Equal(t, 1, res.Attempted)
			require.Zero(t, res.Applied)
			require.Empty(t, res.Journal)
			require.Zero(t, driver.PhysicalWriteCount())
			require.False(t, res.Published)
			require.Equal(t, ReplanSafeNoPhysicalWrites, res.ReplanDisposition)
		})
	}
}

func TestTopologyCoordinatorResetStaleAfterPhysicalWriteDisallowsReplan(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	driver.nodes["primary"].cpus = machine.MustParse("0-1")
	driver.nodes["primary"].configuredCPUs = machine.MustParse("0-1")
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationWriteCPUs && rel == "primary" {
			return ErrCgroupIdentityChanged
		}
		return nil
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, Mems: "0", Mode: ResetModeGuard(),
		Budget: ConvergenceBudget{MaxHierarchyIOOperations: 1000},
	})

	require.ErrorIs(t, err, ErrCgroupIdentityChanged)
	require.Equal(t, 1, res.Attempted)
	require.Zero(t, res.Applied)
	require.Empty(t, res.Journal)
	require.Equal(t, 1, driver.PhysicalWriteCount(), "cpuset.mems must be the only committed write")
	require.False(t, res.Published)
	require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
}

func TestTopologyCoordinatorResetModeAutoBudgetBoundsPostSnapshotDescendantChurn(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0", TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG() error = %v", err)
	}
	driver := newFakeHierarchyDriver()
	driver.allowUnwitnessedExpansion = true
	driver.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0-3", "0")
	listPrimaryCalls := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op != HierarchyOperationList || rel != "primary" {
			return nil
		}
		listPrimaryCalls++
		if listPrimaryCalls != 2 {
			return nil
		}
		for i := 0; i < 128; i++ {
			childRel := fmt.Sprintf("primary/churn-%03d", i)
			driver.add(childRel, CgroupIdentity{Device: 1, Inode: uint64(i + 2)}, "0", "0")
		}
		return nil
	}
	cg := newTopologyFakeCgroup()
	provider := &coordinatorSnapshotTestCgroup{topologyFakeCgroup: cg, driver: driver}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:    dag,
		Cgroup: provider,
		Mems:   "0",
		Mode:   ResetModeGuard(),
		Budget: ConvergenceBudget{DeadlineDuration: -1},
	})
	if !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("Converge(reset) error = %v, want %v", err, ErrHierarchyIOOperationBudgetExceeded)
	}
}

func newCoordinatorSnapshotTestFixture(t *testing.T) (*TopoDAG, *coordinatorSnapshotTestCgroup, *fakeHierarchyDriver) {
	t.Helper()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(0), Mems: "0", TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	driver := newFakeHierarchyDriver()
	driver.add("primary", CgroupIdentity{Device: 1, Inode: 1}, "0", "0")
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	return dag, &coordinatorSnapshotTestCgroup{topologyFakeCgroup: cg, driver: driver}, driver
}

func TestTopologyCoordinatorInitialSnapshotReturnsStaleWithoutInternalRetry(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	staleFailures := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationStat && rel == "primary" && staleFailures == 0 {
			staleFailures++
			return syscall.ENOENT
		}
		return nil
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, CPUDetails: machine.CPUDetails{0: {}},
	})
	var snapshotErr *SnapshotError
	if !errors.As(err, &snapshotErr) || snapshotErr.Class != HierarchyErrorStale {
		t.Fatalf("Converge error = %v, want typed stale", err)
	}
	if staleFailures != 1 || res.Converged || res.ReplanDisposition != ReplanSafeNoPhysicalWrites {
		t.Fatalf("staleFailures=%d result=%+v, want one owner-visible stale scan", staleFailures, res)
	}
}

func TestTopologyCoordinatorInitialSnapshotGenerationChurnReturnsFirstStale(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	staleFailures := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationStat && rel == "primary" && staleFailures < 3 {
			staleFailures++
			driver.bumpIdentity(rel)
			return ErrCgroupIdentityChanged
		}
		return nil
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, CPUDetails: machine.CPUDetails{0: {}},
		Budget: ConvergenceBudget{MaxHierarchyIOOperations: 100},
	})
	var snapshotErr *SnapshotError
	if !errors.As(err, &snapshotErr) || snapshotErr.Class != HierarchyErrorStale {
		t.Fatalf("Converge error = %v, want typed stale", err)
	}
	if staleFailures != 1 || res.Converged || res.ReplanDisposition != ReplanSafeNoPhysicalWrites {
		t.Fatalf("staleFailures=%d result=%+v, want first stale returned to owner", staleFailures, res)
	}
}

func TestTopologyCoordinatorInitialSnapshotGenerationChurnDoesNotHideBehindAutoIOBudget(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	statCalls := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op != HierarchyOperationStat || rel != "primary" {
			return nil
		}
		statCalls++
		if statCalls >= 1000 {
			cancel()
		}
		return ErrCgroupIdentityChanged
	}

	_, err := (TopologyCoordinator{}).Converge(ctx, CoordinatorInput{
		DAG: dag, Cgroup: cg, CPUDetails: machine.CPUDetails{0: {}},
		Budget: ConvergenceBudget{
			MaxSnapshotNodes: 1,
			DeadlineDuration: time.Minute,
		},
	})
	var snapshotErr *SnapshotError
	if !errors.As(err, &snapshotErr) || snapshotErr.Class != HierarchyErrorStale {
		t.Fatalf("Converge error = %v after %d stat calls, want typed stale",
			err, statCalls)
	}
	if statCalls != 1 {
		t.Fatalf("initial snapshot churn statCalls=%d, want one", statCalls)
	}
}

func TestTopologyCoordinatorPreWriteStaleAuthorizesNoWriteReplan(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	delete(driver.nodes, "primary")
	published := 0

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, CPUDetails: machine.CPUDetails{0: {}},
		Budget: ConvergenceBudget{MaxHierarchyIOOperations: 1000},
		PublishFinalSnapshot: func(*CompleteSnapshot) error {
			published++
			return nil
		},
	})
	var snapshotErr *SnapshotError
	if !errors.As(err, &snapshotErr) || snapshotErr.Class != HierarchyErrorStale ||
		snapshotErr.Rel != "primary" {
		t.Fatalf("Converge error = %T %v, want stale SnapshotError for missing configured rel", err, err)
	}
	if res.Converged || res.State != ConvergenceStateNonConverged || res.FinalSnapshotCurrent || published != 0 {
		t.Fatalf("result=%+v published=%d, want non-converged stale result without publication", res, published)
	}
	if res.Published || res.ReplanDisposition != ReplanSafeNoPhysicalWrites {
		t.Fatalf("result=%+v, pre-write stale must authorize only a no-write replan", res)
	}
	if driver.calls > 2 {
		t.Fatalf("hierarchy calls = %d, want one bounded retry rather than scanning to deadline", driver.calls)
	}
}

func TestTopologyCoordinatorMaterializedConfiguredRelDisappearanceBlocksPublication(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	reads := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "primary" {
			reads++
			if reads == 3 {
				delete(driver.nodes, rel)
				return syscall.ENOENT
			}
		}
		return nil
	}
	published := 0

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, CPUDetails: machine.CPUDetails{0: {}},
		Budget: ConvergenceBudget{MaxHierarchyIOOperations: 1000},
		PublishFinalSnapshot: func(*CompleteSnapshot) error {
			published++
			return nil
		},
	})
	var snapshotErr *SnapshotError
	if !errors.As(err, &snapshotErr) || snapshotErr.Class != HierarchyErrorStale {
		t.Fatalf("Converge error = %T %v, want stale SnapshotError after rel disappearance", err, err)
	}
	if res.Converged || res.State != ConvergenceStateNonConverged || res.FinalSnapshotCurrent ||
		res.FinalSnapshot != nil || published != 0 {
		t.Fatalf("result=%+v published=%d, disappeared configured rel must forbid success publication", res, published)
	}
	if reads != 3 {
		t.Fatalf("configured rel reads = %d, want stop immediately after materialized rel disappears", reads)
	}
}

func TestTopologyCoordinatorRoundSnapshotReturnsStaleWithoutInternalRetry(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	primaryReads := 0
	staleFailures := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "primary" {
			primaryReads++
		}
		if op == HierarchyOperationList && rel == "primary" && primaryReads == 2 && staleFailures == 0 {
			staleFailures++
			return syscall.ENOENT
		}
		return nil
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, CPUDetails: machine.CPUDetails{0: {}},
	})
	var snapshotErr *SnapshotError
	if !errors.As(err, &snapshotErr) || snapshotErr.Class != HierarchyErrorStale {
		t.Fatalf("Converge error = %v, want typed stale", err)
	}
	if staleFailures != 1 || res.Converged || res.ReplanDisposition != ReplanSafeNoPhysicalWrites {
		t.Fatalf("staleFailures=%d result=%+v, want one stale returned to owner", staleFailures, res)
	}
}

func TestTopologyCoordinatorMidRoundCancelReleasesModeForNextInvocation(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	gate := NewModeGate()
	ctx, cancel := context.WithCancel(context.Background())
	primaryReads := 0
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "primary" {
			primaryReads++
			if primaryReads == 2 {
				cancel()
				return ctx.Err()
			}
		}
		return nil
	}

	_, err := (TopologyCoordinator{}).Converge(ctx, CoordinatorInput{
		DAG: dag, Cgroup: cg, Mode: NormalModeGuardWithGate(gate),
		CPUDetails: machine.CPUDetails{0: {}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("first Converge error = %T %v, want context.Canceled", err, err)
	}

	driver.beforeCall = nil
	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, Mode: NormalModeGuardWithGate(gate),
		CPUDetails: machine.CPUDetails{0: {}},
	})
	if err != nil {
		t.Fatalf("second Converge after cancellation: %v", err)
	}
	if !res.Converged || !res.FinalSnapshotCurrent {
		t.Fatalf("second result = %+v, want recovered convergence", res)
	}
}

func TestTopologyCoordinatorHardPublicationErrorDisallowsReplan(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	gate := NewModeGate()
	publishErr := errors.New("publish failed")
	publishCalls := 0
	publish := func(*CompleteSnapshot) error {
		publishCalls++
		return publishErr
	}
	input := CoordinatorInput{
		DAG: dag, Cgroup: cg, Mode: NormalModeGuardWithGate(gate),
		CPUDetails: machine.CPUDetails{0: {}}, PublishFinalSnapshot: publish,
	}

	first, err := (TopologyCoordinator{}).Converge(context.Background(), input)
	if !errors.Is(err, publishErr) {
		t.Fatalf("first Converge error = %T %v, want publish failure", err, err)
	}
	if first.Published || first.ReplanDisposition != ReplanNotAllowed {
		t.Fatalf("first result = %+v, hard publication failure must terminate", first)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("first converged publication attempt wrote hierarchy: %#v", driver.writes)
	}
	if publishCalls != 1 {
		t.Fatalf("publish calls = %d, want exactly one", publishCalls)
	}
}

func TestTopologyCoordinatorPublicationStalePreservesVerifiedFinalState(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	publishCalls := 0
	lifecycleErr := errors.New("container leaf changed before publication")
	publishErr := &PlanStaleError{
		Rel:       "primary/container",
		Direction: WritePublish,
		Resource:  "container_cpuset",
		Current:   "0",
		Target:    "1",
		Err:       lifecycleErr,
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, CPUDetails: machine.CPUDetails{0: {}},
		PublishFinalSnapshot: func(snapshot *CompleteSnapshot) error {
			publishCalls++
			if snapshot == nil {
				return errors.New("nil final snapshot")
			}
			snapshot.Entries["primary"] = EntryState{
				Rel:            "primary",
				Identity:       snapshot.Entries["primary"].Identity,
				CPUs:           machine.NewCPUSet(9),
				ConfiguredCPUs: machine.NewCPUSet(9),
				Mems:           "9",
				ConfiguredMems: "9",
			}
			return publishErr
		},
	})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("Converge error = %T %v, want publication stale", err, err)
	}
	require.ErrorIs(t, err, lifecycleErr, "publication stale must preserve the production lifecycle error chain")
	if !res.Converged || !res.FinalSnapshotCurrent || res.FinalSnapshot == nil {
		t.Fatalf("result = %+v, want verified current final snapshot preserved", res)
	}
	require.Equal(t, "0", res.FinalSnapshot.Entries["primary"].CPUs.String(),
		"publication callback must not mutate the verified result snapshot")
	if res.Published || res.ReplanDisposition != ReplanSafeFromVerifiedFinalState {
		t.Fatalf("result = %+v, want unpublished safe-final-state replan", res)
	}
	if publishCalls != 1 {
		t.Fatalf("publish calls = %d, want no internal retry", publishCalls)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("stale publication wrote hierarchy: %#v", driver.writes)
	}
}

func TestTopologyCoordinatorParentSafePublicationStalePreservesZeroWriteFinalSnapshot(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	publishCalls := 0
	lifecycleErr := errors.New("container lifecycle changed")
	publishErr := fmt.Errorf("publish current snapshot: %w",
		&PlanStaleError{
			Rel:       "primary/container",
			Direction: WritePublish,
			Resource:  "container_cpuset",
			Current:   "0",
			Target:    "1",
			Err:       lifecycleErr,
		})

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, Objective: ConvergenceObjectiveParentSafe,
		CPUDetails: machine.CPUDetails{0: {}},
		PublishFinalSnapshot: func(snapshot *CompleteSnapshot) error {
			publishCalls++
			require.NotNil(t, snapshot)
			entry := snapshot.Entries["primary"]
			entry.CPUs.Add(9)
			snapshot.Entries["primary"] = entry
			return publishErr
		},
	})

	require.ErrorIs(t, err, ErrCoordinatorPlanStale)
	require.ErrorIs(t, err, lifecycleErr)
	require.Equal(t, 1, publishCalls, "ParentSafe publication stale must not retry internally")
	require.True(t, res.Converged)
	require.False(t, res.ParentSafe)
	require.True(t, res.FinalSnapshotCurrent)
	require.NotNil(t, res.FinalSnapshot)
	require.Equal(t, "0", res.FinalSnapshot.Entries["primary"].CPUs.String(),
		"ParentSafe result must retain an independent verified snapshot")
	require.False(t, res.Published)
	require.Equal(t, ReplanSafeFromVerifiedFinalState, res.ReplanDisposition)
	require.Empty(t, driver.writes, "already-converged ParentSafe publication must perform zero hierarchy writes")
}

func TestTopologyCoordinatorPublicationDoesNotGeneralizeReplanMarkers(t *testing.T) {
	t.Parallel()
	for _, objective := range []ConvergenceObjective{
		ConvergenceObjectiveFull,
		ConvergenceObjectiveParentSafe,
	} {
		objective := objective
		t.Run(string(objective), func(t *testing.T) {
			t.Parallel()
			dag, cg, _ := newCoordinatorSnapshotTestFixture(t)
			hardErr := errors.New("publication failed after lifecycle validation")
			markerErr := &publicationReplanMarkerError{err: hardErr}

			res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
				DAG: dag, Cgroup: cg, Objective: objective,
				CPUDetails: machine.CPUDetails{0: {}},
				PublishFinalSnapshot: func(*CompleteSnapshot) error {
					return markerErr
				},
			})

			require.ErrorIs(t, err, hardErr)
			require.False(t, errors.Is(err, ErrCoordinatorPlanStale))
			require.False(t, res.Published)
			require.Equal(t, ReplanNotAllowed, res.ReplanDisposition)
			require.False(t, res.FinalSnapshotCurrent)
			require.Nil(t, res.FinalSnapshot)
		})
	}
}

func TestTopologyCoordinatorPrioritizesStalePlanOverBudget(t *testing.T) {
	t.Parallel()
	stale := &PlanStaleError{
		Rel:       "kubepods/stale",
		Direction: WriteShrink,
		Resource:  "snapshot",
		Current:   "missing",
		Target:    "0-1",
		Err:       syscall.ENOENT,
	}
	err := prioritizeRoundStalePlanError(RoundOutcome{Status: RoundStatusStale, Blocker: stale},
		fmt.Errorf("%w: limit=62288 used=62288", ErrHierarchyIOOperationBudgetExceeded))
	var staleErr *PlanStaleError
	if !errors.As(err, &staleErr) {
		t.Fatalf("prioritized error = %T %v, want PlanStaleError before budget", err, err)
	}
	if errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("prioritized error = %v, should not prioritize hierarchy budget over stale plan", err)
	}
}

func TestTopologyCoordinatorPersistentStaleSnapshotStopsAfterFirstScanWithoutPublishing(t *testing.T) {
	t.Parallel()
	dag, cg, driver := newCoordinatorSnapshotTestFixture(t)
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationStat && rel == "primary" {
			return syscall.ENOENT
		}
		return nil
	}
	published := 0

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg, CPUDetails: machine.CPUDetails{0: {}},
		Budget: ConvergenceBudget{MaxHierarchyIOOperations: 1000},
		PublishFinalSnapshot: func(*CompleteSnapshot) error {
			published++
			return nil
		},
	})
	var snapshotErr *SnapshotError
	if !errors.As(err, &snapshotErr) || snapshotErr.Class != HierarchyErrorStale {
		t.Fatalf("Converge error = %T %v, want stale snapshot failure; result=%+v", err, err, res)
	}
	if driver.calls != 1 {
		t.Fatalf("underlying hierarchy calls = %d, want no internal retry", driver.calls)
	}
	if published != 0 || res.Converged || res.State != ConvergenceStateNonConverged ||
		res.FinalSnapshot != nil || res.FinalSnapshotCurrent || len(res.Rounds) != 0 {
		t.Fatalf("persistent stale published state: published=%d result=%+v", published, res)
	}
}

func TestCoordinatorRoundNextSnapshotDoesNotRetryNonStaleErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "invalid",
			err:  &SnapshotError{Operation: HierarchyOperationRead, Class: HierarchyErrorInvalid, Err: errors.New("invalid")},
		},
		{
			name: "budget",
			err:  &SnapshotError{Operation: HierarchyOperationStat, Class: HierarchyErrorBudget, Err: ErrHierarchyIOOperationBudgetExceeded},
		},
		{
			name: "permission",
			err:  &SnapshotError{Operation: HierarchyOperationList, Class: HierarchyErrorInvalid, Err: syscall.EACCES},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			round := &coordinatorRound{
				budget: NewBudgetTracker(ConvergenceBudget{}),
				snapshotSource: func(context.Context) (*CompleteSnapshot, error) {
					calls++
					return nil, tc.err
				},
			}
			_, err := round.nextSnapshot(context.Background())
			if !errors.Is(err, tc.err) {
				t.Fatalf("nextSnapshot error = %T %v, want original %T %v", err, err, tc.err, tc.err)
			}
			if calls != 1 {
				t.Fatalf("snapshot calls = %d, want no retry", calls)
			}
		})
	}
}

func TestCoordinatorRoundNextSnapshotReturnsFirstMissingGeneration(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0), TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	calls := 0
	round := &coordinatorRound{
		dag:    dag,
		budget: NewBudgetTracker(ConvergenceBudget{}),
		snapshotSource: func(context.Context) (*CompleteSnapshot, error) {
			calls++
			if calls <= 2 {
				return nil, &SnapshotError{
					Operation: HierarchyOperationRead,
					Rel:       "primary",
					Class:     HierarchyErrorStale,
					Identity:  CgroupIdentity{Device: 1, Inode: uint64(calls)},
					Err:       syscall.ENOENT,
				}
			}
			return &CompleteSnapshot{}, nil
		},
	}

	if _, err := round.nextSnapshot(context.Background()); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("nextSnapshot error = %v, want first missing generation", err)
	}
	if calls != 1 {
		t.Fatalf("snapshot calls = %d, want no generation merge or internal retry", calls)
	}
}

func TestCoordinatorRoundNextSnapshotReturnsConfiguredRelStaleImmediately(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0), TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	tests := []struct {
		name     string
		staleErr error
	}{
		{name: "ENOENT", staleErr: syscall.ENOENT},
		{name: "ENOTDIR", staleErr: syscall.ENOTDIR},
		{name: "ENODEV", staleErr: syscall.ENODEV},
		{name: "other stale churn", staleErr: syscall.EBUSY},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			round := &coordinatorRound{
				dag:    dag,
				budget: NewBudgetTracker(ConvergenceBudget{}),
				snapshotSource: func(context.Context) (*CompleteSnapshot, error) {
					calls++
					if calls > 2 {
						return &CompleteSnapshot{}, nil
					}
					return nil, &SnapshotError{
						Operation: HierarchyOperationRead,
						Rel:       "primary",
						Class:     HierarchyErrorStale,
						Identity:  CgroupIdentity{Device: 1, Inode: 11},
						Err:       tc.staleErr,
					}
				},
			}

			_, err := round.nextSnapshot(context.Background())
			if !errors.Is(err, tc.staleErr) {
				t.Fatalf("nextSnapshot error = %v, want %v", err, tc.staleErr)
			}
			if calls != 1 {
				t.Fatalf("snapshot calls = %d, want one", calls)
			}
		})
	}
}

func TestCoordinatorRoundNextSnapshotDoesNotMergeSameGenerationEvidence(t *testing.T) {
	t.Parallel()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, CPUs: machine.NewCPUSet(0), TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	calls := 0
	round := &coordinatorRound{
		dag:    dag,
		budget: NewBudgetTracker(ConvergenceBudget{}),
		snapshotSource: func(context.Context) (*CompleteSnapshot, error) {
			calls++
			if calls > 2 {
				return nil, &SnapshotError{
					Operation: HierarchyOperationRead,
					Rel:       "primary",
					Class:     HierarchyErrorInvalid,
					Err:       errors.New("same generation was not detected"),
				}
			}
			stale := &SnapshotError{
				Operation: HierarchyOperationRead,
				Rel:       "primary",
				Class:     HierarchyErrorStale,
				Identity:  CgroupIdentity{Device: 1, Inode: 11},
				Err:       syscall.ENOENT,
			}
			stale.EvidenceID[0] = byte(calls)
			return nil, stale
		},
	}

	if _, err := round.nextSnapshot(context.Background()); !errors.Is(err, syscall.ENOENT) {
		t.Fatalf("nextSnapshot error = %v, want repeated missing generation", err)
	}
	if calls != 1 {
		t.Fatalf("snapshot calls = %d, want first evidence returned to owner", calls)
	}
}

func TestTopologyCoordinatorPlanBudgetErrorDoesNotStartWrites(t *testing.T) {
	t.Parallel()

	dag, cg := benchmarkTwoDomainSwapFixture(t, benchmarkCPUDetails(2, 4))
	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:            dag,
		Cgroup:         cg,
		CPUDetails:     benchmarkCPUDetails(2, 4),
		ReservedCPUSet: machine.NewCPUSet(),
		Budget: ConvergenceBudget{
			MaxPlanOperations: 1,
		},
	})
	if !errors.Is(err, ErrPlanOperationBudgetExceeded) {
		t.Fatalf("Converge error = %v, want ErrPlanOperationBudgetExceeded; result=%+v", err, res)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("plan budget failure must not start writes, writes=%#v", cg.writes)
	}
	for _, round := range res.Rounds {
		if len(round.Witnesses) != 0 {
			t.Fatalf("budget failure published state: %+v", round)
		}
	}
	if res.FinalSnapshot != nil || res.FinalSnapshotCurrent {
		t.Fatalf("budget failure published final snapshot: %+v", res)
	}
}

func TestTopologyCoordinatorDeadlineBudgetDoesNotPublishState(t *testing.T) {
	t.Parallel()

	dag, cg := benchmarkTwoDomainSwapFixture(t, benchmarkCPUDetails(2, 4))
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	res, err := (TopologyCoordinator{}).Converge(ctx, CoordinatorInput{
		DAG:            dag,
		Cgroup:         cg,
		CPUDetails:     benchmarkCPUDetails(2, 4),
		ReservedCPUSet: machine.NewCPUSet(),
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Converge error = %v, want context deadline exceeded; result=%+v", err, res)
	}
	if len(cg.writes) != 0 || len(res.Rounds) != 0 {
		t.Fatalf("deadline budget failure wrote or published rounds, writes=%#v result=%+v", cg.writes, res)
	}
}

func TestTopologyCoordinatorNormalModeExpiredExplicitDeadlineFailsBeforeIO(t *testing.T) {
	t.Parallel()

	dag, cg := benchmarkTwoDomainSwapFixture(t, benchmarkCPUDetails(2, 4))
	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:            dag,
		Cgroup:         cg,
		CPUDetails:     benchmarkCPUDetails(2, 4),
		ReservedCPUSet: machine.NewCPUSet(),
		Budget: ConvergenceBudget{
			Deadline:         time.Now().Add(-time.Second),
			DeadlineDuration: time.Hour,
		},
	})
	if !errors.Is(err, ErrConvergenceDeadlineExceeded) {
		t.Fatalf("Converge error = %v, want ErrConvergenceDeadlineExceeded; result=%+v", err, res)
	}
	if cg.reads != 0 || cg.snapshotRootReads != 0 || len(cg.writes) != 0 || len(res.Rounds) != 0 {
		t.Fatalf("expired explicit deadline must fail before hierarchy I/O or rounds, reads=%d snapshotRootReads=%d writes=%#v result=%+v",
			cg.reads, cg.snapshotRootReads, cg.writes, res)
	}
}

func TestTopologyCoordinatorResetModeExpiredExplicitDeadlineFailsBeforeIO(t *testing.T) {
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
			Deadline:         time.Now().Add(-time.Second),
			DeadlineDuration: time.Hour,
		},
	})
	if !errors.Is(err, ErrConvergenceDeadlineExceeded) {
		t.Fatalf("Converge error = %v, want ErrConvergenceDeadlineExceeded; result=%+v", err, res)
	}
	if cg.reads != 0 || cg.snapshotRootReads != 0 || len(cg.writes) != 0 {
		t.Fatalf("expired explicit deadline must fail before hierarchy I/O, reads=%d snapshotRootReads=%d writes=%#v result=%+v",
			cg.reads, cg.snapshotRootReads, cg.writes, res)
	}
}

func TestTopologyCoordinatorSmallStepMatchesFullDrainFinalPartition(t *testing.T) {
	t.Parallel()

	details := benchmarkCPUDetails(2, 4)
	run := func(selection DrainSelectionPolicy) (map[string]machine.CPUSet, int) {
		t.Helper()
		dag, cg := benchmarkTwoDomainSwapFixture(t, details)
		res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
			DAG:            dag,
			Cgroup:         cg,
			CPUDetails:     details,
			ReservedCPUSet: machine.NewCPUSet(),
			DrainSelection: selection,
		})
		if err != nil {
			t.Fatalf("Converge: %v", err)
		}
		if !res.Converged {
			t.Fatalf("result = %+v, want converged", res)
		}
		return model.CloneCPUSetMap(cg.cpus), len(res.Rounds)
	}
	full, fullRounds := run(DrainSelectionPolicy{MaxCPUsDrainRatio: 0, GroupByNUMA: true, RequirePairedSwapProgress: true})
	small, smallRounds := run(DrainSelectionPolicy{MaxCPUsDrainRatio: 0.25, GroupByNUMA: true, RequirePairedSwapProgress: true})
	if !full["primary"].Equals(small["primary"]) || !full["reclaim"].Equals(small["reclaim"]) {
		t.Fatalf("small-step partition = primary:%s reclaim:%s, want full primary:%s reclaim:%s",
			small["primary"].String(), small["reclaim"].String(), full["primary"].String(), full["reclaim"].String())
	}
	if smallRounds < fullRounds {
		t.Fatalf("small-step rounds = %d, full rounds = %d; want small-step not to bypass full-drain phases", smallRounds, fullRounds)
	}
}

func TestTopologyCoordinatorAdaptiveRoundBudgetConvergesFull96CPUHandoff(t *testing.T) {
	t.Parallel()

	details := benchmarkCPUDetails(1, 96)
	allCPUs := details.CPUs()
	dag, err := BuildDAG([]NodeSpec{
		{
			Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
			CPUs: machine.NewCPUSet(), Mems: "0", TrustAnchor: true,
		},
		{
			Rel: "reclaim", Domain: DomainReclaim, Role: TopoNodeRoleReclaim,
			CPUs: allCPUs, Mems: "0", TrustAnchor: true,
		},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.version = cgroupclient.CgroupVersionV2
	cg.cpus["primary"] = allCPUs
	cg.cpus["reclaim"] = machine.NewCPUSet()

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: details,
		DrainSelection: DrainSelectionPolicy{
			MaxCPUsDrainRatio:         0.01,
			RequirePairedSwapProgress: true,
		},
		Budget: ConvergenceBudget{DeadlineDuration: -1},
	})
	if err != nil {
		t.Fatalf("Converge: %v; rounds=%d state=%s", err, len(res.Rounds), res.State)
	}
	if !res.Converged || res.State != ConvergenceStateConverged {
		t.Fatalf("result = %+v, want converged in one invocation", res)
	}
	if got := len(res.Rounds); got <= 32 {
		t.Fatalf("rounds = %d, want workload-derived budget to permit more than old fixed default 32", got)
	}
	if got := cg.cpus["primary"]; !got.IsEmpty() {
		t.Fatalf("final primary CPUs = %s, want empty", got.String())
	}
	if got := cg.cpus["reclaim"]; !got.Equals(allCPUs) {
		t.Fatalf("final reclaim CPUs = %s, want %s", got.String(), allCPUs.String())
	}
}

func TestCoordinatorAdaptiveRoundBudgetPreservesExplicitLimitAndAllowsLargeWorkload(t *testing.T) {
	t.Parallel()

	details := benchmarkCPUDetails(1, 1024)
	allCPUs := details.CPUs()
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(), TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, Role: TopoNodeRoleReclaim, CPUs: allCPUs, TrustAnchor: true},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	input := PhasePlanInput{
		Kind: PhaseDrain,
		DAG:  dag,
		Snapshot: planSnapshot(
			map[string]EntryState{
				"primary": {Rel: "primary", CPUs: allCPUs},
				"reclaim": {Rel: "reclaim", CPUs: machine.NewCPUSet()},
			},
			map[DomainID]machine.CPUSet{
				DomainPrimary: allCPUs,
				DomainReclaim: machine.NewCPUSet(),
			},
		),
		DesiredByRel: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(),
			"reclaim": allCPUs,
		},
		CPUDetails: details,
		Selection:  DrainSelectionPolicy{MaxCPUsDrainRatio: 0.001},
	}
	if got := coordinatorMaxRoundsForPlanInput(input, 7); got != 7 {
		t.Fatalf("explicit MaxRounds = %d, want 7", got)
	}
	if got := coordinatorMaxRoundsForPlanInput(input, 0); got != 513 {
		t.Fatalf("adaptive MaxRounds = %d, want 513 for 1024 CPUs at ratio 0.001", got)
	}
}

func TestCoordinatorAdaptiveRoundBudgetIncludesCleanupOnlyLeavingWork(t *testing.T) {
	t.Parallel()

	details := benchmarkCPUDetails(1, 96)
	allCPUs := details.CPUs()
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(), TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(), TrustAnchor: true},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	input := PhasePlanInput{
		Kind: PhaseDrain,
		DAG:  dag,
		Snapshot: planSnapshot(
			map[string]EntryState{
				"primary": {Rel: "primary", CPUs: allCPUs},
				"reclaim": {Rel: "reclaim", CPUs: machine.NewCPUSet()},
			},
			map[DomainID]machine.CPUSet{
				DomainPrimary: allCPUs,
				DomainReclaim: machine.NewCPUSet(),
			},
		),
		DesiredByRel: map[string]machine.CPUSet{
			"primary": machine.NewCPUSet(),
			"reclaim": machine.NewCPUSet(),
		},
		CPUDetails: details,
		Selection:  DrainSelectionPolicy{MaxCPUsDrainRatio: 0.01},
	}
	if got := coordinatorMaxRoundsForPlanInput(input, 0); got != 49 {
		t.Fatalf("cleanup-only adaptive MaxRounds = %d, want 49", got)
	}
}

func TestTopologyCoordinatorAdaptiveRoundBudgetConverges96CPUCleanupOnly(t *testing.T) {
	t.Parallel()

	details := benchmarkCPUDetails(1, 96)
	allCPUs := details.CPUs()
	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(), TrustAnchor: true},
		{Rel: "reclaim", Domain: DomainReclaim, Role: TopoNodeRoleReclaim, CPUs: machine.NewCPUSet(), TrustAnchor: true},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.version = cgroupclient.CgroupVersionV2
	cg.cpus["primary"] = allCPUs
	cg.cpus["reclaim"] = machine.NewCPUSet()

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: details,
		DrainSelection: DrainSelectionPolicy{
			MaxCPUsDrainRatio:         0.01,
			RequirePairedSwapProgress: true,
		},
		Budget: ConvergenceBudget{DeadlineDuration: -1},
	})
	if err != nil {
		t.Fatalf("Converge cleanup-only: %v; rounds=%d state=%s", err, len(res.Rounds), res.State)
	}
	if !res.Converged {
		t.Fatalf("cleanup-only result = %+v, want converged", res)
	}
	if got := cg.cpus["primary"]; !got.IsEmpty() {
		t.Fatalf("final primary CPUs = %s, want empty", got.String())
	}
	if got := len(res.Rounds); got <= 32 {
		t.Fatalf("cleanup-only rounds = %d, want to exceed old fixed default 32", got)
	}
}

func TestTopologyCoordinatorAutoBudgetConvergesProductionHierarchyShape(t *testing.T) {
	t.Parallel()
	const (
		cpuCount        = 101
		handoffCPUCount = 100
		numaBucketCount = 8
		descendantCount = 174
	)

	details := benchmarkCPUDetails(numaBucketCount, cpuCount/numaBucketCount)
	for cpu := len(details); cpu < cpuCount; cpu++ {
		details[cpu] = machine.CPUTopoInfo{
			NUMANodeID: cpu % numaBucketCount,
			SocketID:   cpu % numaBucketCount,
			CoreID:     cpu,
		}
	}
	allCPUs := details.CPUs()
	handoffCPUs := machine.NewCPUSet()
	for cpu := 0; cpu < handoffCPUCount; cpu++ {
		handoffCPUs.Add(cpu)
	}
	specs := []NodeSpec{
		{
			Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
			CPUs: machine.NewCPUSet(handoffCPUCount), Mems: "0", ControlledRoot: true, TrustAnchor: true,
		},
		{
			Rel: "reclaim", Domain: DomainReclaim, Role: TopoNodeRoleReclaim,
			CPUs: handoffCPUs, Mems: "0", ControlledRoot: true, TrustAnchor: true,
		},
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = allCPUs
	cg.cpus["reclaim"] = machine.NewCPUSet()
	cg.files["primary"] = map[string][]byte{"cpuset.mems": []byte("0")}
	cg.files["reclaim"] = map[string][]byte{"cpuset.mems": []byte("0")}

	for numa := 0; numa < numaBucketCount; numa++ {
		rel := fmt.Sprintf("reclaim/numa-%d", numa)
		target := machine.NewCPUSet()
		for cpu, info := range details {
			if cpu < handoffCPUCount && info.NUMANodeID == numa {
				target.Add(cpu)
			}
		}
		specs = append(specs, NodeSpec{
			Rel: rel, ParentRel: "reclaim", Domain: DomainReclaim,
			Role: TopoNodeRoleReclaimNUMABucket, CPUs: target, Mems: "0",
			TrustAnchor: true,
			Constraint: TopologyConstraint{
				CPUUpperBound: target.Clone(),
				Scope:         TopologyScopeNUMANode,
			},
			Metadata: map[string]string{"numa": strconv.Itoa(numa)},
		})
		cg.cpus[rel] = machine.NewCPUSet()
		cg.files[rel] = map[string][]byte{"cpuset.mems": []byte("0")}
		cg.children["reclaim"] = append(cg.children["reclaim"], fmt.Sprintf("numa-%d", numa))
	}
	for i := 0; i < descendantCount; i++ {
		name := fmt.Sprintf("pod-%03d", i)
		rel := "primary/" + name
		cg.children["primary"] = append(cg.children["primary"], name)
		cg.cpus[rel] = machine.NewCPUSet(i % handoffCPUCount)
		cg.files[rel] = map[string][]byte{"cpuset.mems": []byte("0")}
	}
	dag, err := BuildDAG(specs)
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: details,
		Mems:       "0",
		DrainSelection: DrainSelectionPolicy{
			MaxCPUsDrainRatio:         0.01,
			RequirePairedSwapProgress: true,
		},
		Budget: ConvergenceBudget{DeadlineDuration: -1},
	})
	if err != nil {
		if !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
			t.Fatalf("Converge error = %v, want successful convergence (old code must fail with typed hierarchy I/O budget error)", err)
		}
		t.Fatalf("Converge exhausted auto hierarchy I/O budget: %v; rounds=%d writes=%d", err, len(res.Rounds), len(cg.writes))
	}
	if !res.Converged || !res.FinalSnapshotCurrent {
		t.Fatalf("Converge result = %+v, want current converged snapshot", res)
	}
	if got := len(res.FinalSnapshot.Entries); got != 2+numaBucketCount+descendantCount {
		t.Fatalf("final snapshot entries = %d, want %d", got, 2+numaBucketCount+descendantCount)
	}
}

func TestBudgetTrackerDerivesCumulativeAutoLimitsFromRoundsAndSnapshotSize(t *testing.T) {
	t.Parallel()

	budget := NewBudgetTracker(ConvergenceBudget{
		MaxSnapshotNodes: 4096,
		MaxSnapshotDepth: 16,
	})
	if err := budget.configureAutoCumulativeLimitsFromInput(AutoCumulativeBudgetInput{
		RemainingRounds:          513,
		SnapshotIOUpperBound:     1000,
		MaxDrainFrontiersTotal:   513,
		MaxGrowDomainsTotal:      513,
		MaxPlanOperationsTotal:   513 * 1000,
		MaxChildMembershipsTotal: 1000,
		StaleRetryAllowance:      513,
	}); err != nil {
		t.Fatalf("configure auto limits: %v", err)
	}
	if budget.limit.MaxRounds != 513 {
		t.Fatalf("round limit = %d, want 513", budget.limit.MaxRounds)
	}
	if budget.limit.MaxHierarchyIOOperations < 513*1000 {
		t.Fatalf("hierarchy I/O limit = %d, want conservative cumulative headroom", budget.limit.MaxHierarchyIOOperations)
	}
	if budget.limit.MaxPlanOperations < 513*1000 {
		t.Fatalf("plan operation limit = %d, want conservative cumulative headroom", budget.limit.MaxPlanOperations)
	}
}

func TestBudgetTrackerBootstrapAccountsForDormantActivityWorstCase(t *testing.T) {
	t.Parallel()

	const (
		nodes        = 17
		staleRetries = 3
	)
	normal := NewBudgetTracker(ConvergenceBudget{MaxSnapshotNodes: nodes})
	if err := normal.configureAutoHierarchyIOBootstrap(staleRetries, false); err != nil {
		t.Fatalf("configure normal bootstrap: %v", err)
	}
	activity := NewBudgetTracker(ConvergenceBudget{MaxSnapshotNodes: nodes})
	if err := activity.configureAutoHierarchyIOBootstrap(staleRetries, true); err != nil {
		t.Fatalf("configure activity bootstrap: %v", err)
	}

	if got, want := normal.limit.MaxHierarchyIOOperations,
		nodes*normalSnapshotHierarchyIOPerNode+staleRetries+fixedInvocationHierarchyIOHeadroom; got != want {
		t.Fatalf("normal bootstrap I/O limit = %d, want %d", got, want)
	}
	if got, want := activity.limit.MaxHierarchyIOOperations,
		nodes*activitySnapshotHierarchyIOPerNode+staleRetries+fixedInvocationHierarchyIOHeadroom; got != want {
		t.Fatalf("activity bootstrap I/O limit = %d, want %d", got, want)
	}
	if activity.limit.MaxHierarchyIOOperations <= normal.limit.MaxHierarchyIOOperations {
		t.Fatalf("activity bootstrap limit = %d, want greater than normal %d",
			activity.limit.MaxHierarchyIOOperations, normal.limit.MaxHierarchyIOOperations)
	}
}

func TestBudgetTrackerPreservesExplicitNonZeroCumulativeLimits(t *testing.T) {
	t.Parallel()

	budget := NewBudgetTracker(ConvergenceBudget{
		MaxRounds:                7,
		MaxHierarchyIOOperations: 11,
		MaxPlanOperations:        13,
	})
	if err := budget.configureAutoCumulativeLimitsFromInput(AutoCumulativeBudgetInput{
		RemainingRounds:          513,
		SnapshotIOUpperBound:     1000,
		MaxDrainFrontiersTotal:   513,
		MaxGrowDomainsTotal:      513,
		MaxPlanOperationsTotal:   513 * 1000,
		MaxChildMembershipsTotal: 1000,
		StaleRetryAllowance:      513,
	}); err != nil {
		t.Fatalf("configure explicit limits: %v", err)
	}
	if budget.limit.MaxRounds != 7 ||
		budget.limit.MaxHierarchyIOOperations != 11 ||
		budget.limit.MaxPlanOperations != 13 {
		t.Fatalf("explicit limits changed: %+v", budget.limit)
	}
}

func TestBudgetTrackerAutoCumulativeConfigurationIsAtomicOnOverflow(t *testing.T) {
	t.Parallel()

	budget := NewBudgetTracker(ConvergenceBudget{})
	maxInt := int(^uint(0) >> 1)
	err := budget.configureAutoCumulativeLimitsFromInput(AutoCumulativeBudgetInput{
		RemainingRounds:        2,
		SnapshotIOUpperBound:   maxInt,
		MaxPlanOperationsTotal: 17,
	})
	if !errors.Is(err, ErrAutoCumulativeBudgetInvalid) {
		t.Fatalf("configure auto limits error = %v, want %v", err, ErrAutoCumulativeBudgetInvalid)
	}
	if budget.limit.MaxRounds != 0 ||
		budget.limit.MaxHierarchyIOOperations != 0 ||
		budget.limit.MaxPlanOperations != 0 {
		t.Fatalf("failed configuration partially changed limits: %+v", budget.limit)
	}
}

func TestCoordinatorAutoPlanOperationsTotalUsesDepthFrontiersAndRoundsOnce(t *testing.T) {
	t.Parallel()

	snapshot := &CompleteSnapshot{
		Entries: map[string]EntryState{
			"root":         {},
			"root/child":   {},
			"root/child/x": {},
		},
		Children: map[string][]ChildRef{
			"root":       {{Name: "child"}},
			"root/child": {{Name: "x"}},
		},
	}
	got, err := coordinatorAutoPlanOperationsTotal(2, snapshot)
	if err != nil {
		t.Fatalf("coordinatorAutoPlanOperationsTotal: %v", err)
	}
	// Per round: initial drain projection+operations = 3+2+3,
	// frontier rebases = 2+1+0, and expand = 3.
	if want := 2 * (8 + 3 + 3); got != want {
		t.Fatalf("plan operations total = %d, want %d", got, want)
	}
}

func TestBudgetTrackerAutoCumulativeConfigurationPreservesExplicitPlanLimit(t *testing.T) {
	t.Parallel()

	budget := NewBudgetTracker(ConvergenceBudget{MaxPlanOperations: 7})
	err := budget.configureAutoCumulativeLimitsFromInput(AutoCumulativeBudgetInput{
		RemainingRounds:        2,
		SnapshotIOUpperBound:   3,
		MaxPlanOperationsTotal: 100,
	})
	if err != nil {
		t.Fatalf("configure auto limits: %v", err)
	}
	if got := budget.limit.MaxPlanOperations; got != 7 {
		t.Fatalf("explicit plan operation limit = %d, want 7", got)
	}
	if err := budget.ConsumePlanOperations(8); !errors.Is(err, ErrPlanOperationBudgetExceeded) {
		t.Fatalf("explicit plan limit overflow error = %v, want %v", err, ErrPlanOperationBudgetExceeded)
	}
}

func TestCoordinatorAutoCumulativeBudgetInputReturnsTypedPlanOverflow(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "root", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0),
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	snapshot := &CompleteSnapshot{
		Entries: map[string]EntryState{
			"root":       {},
			"root/child": {},
		},
		Children: map[string][]ChildRef{
			"root": {{Name: "child"}},
		},
		DomainUnion: map[DomainID]machine.CPUSet{
			DomainPrimary: machine.NewCPUSet(0),
		},
		Cost: BudgetUsage{HierarchyIOOperations: 1},
	}
	_, err = coordinatorAutoCumulativeBudgetInput(int(^uint(0)>>1), dag, snapshot, BudgetUsage{})
	if !errors.Is(err, ErrAutoCumulativeBudgetInvalid) {
		t.Fatalf("coordinator auto budget error = %v, want %v", err, ErrAutoCumulativeBudgetInvalid)
	}
}

func TestTopologyCoordinatorConvergeReturnsPostWriteStaleToAdjustmentOwner(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.afterApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == "primary" && len(cg.writes) == 1 {
			cg.cpus[rel] = machine.NewCPUSet(0)
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: machine.CPUDetails{0: {}, 1: {}},
	})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("Converge error = %v, want owner-visible stale", err)
	}
	if res.Converged || res.State != ConvergenceStateNonConverged {
		t.Fatalf("result = %+v, want non-converged stale attempt", res)
	}
	if got := len(res.Rounds); got != 1 {
		t.Fatalf("rounds = %d, want no internal stale recompile; writes=%#v", got, cg.writes)
	}
	if res.Rounds[0].Status != RoundStatusStale {
		t.Fatalf("round status = %s, want %s", res.Rounds[0].Status, RoundStatusStale)
	}
	if res.ReplanDisposition != ReplanNotAllowed ||
		res.FinalSnapshotCurrent || res.FinalSnapshot != nil {
		t.Fatalf("result = %+v, want no cross-attempt authority without an adjustment budget", res)
	}
}

func TestTopologyCoordinatorRevalidatesRequiredIdentityBeforeReplannedWrite(t *testing.T) {
	t.Parallel()

	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	dag, err := BuildDAG([]NodeSpec{
		{
			Rel: "reclaim", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
			CPUs: machine.NewCPUSet(0), Mems: "0", TrustAnchor: true,
		},
		{
			Rel: "reclaim/child", ParentRel: "reclaim", Domain: DomainPrimary,
			Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0), Mems: "0",
		},
	})
	if err != nil {
		t.Fatalf("BuildDAG() error: %v", err)
	}
	driver := newFakeHierarchyDriver()
	driver.add("reclaim", rootIdentity, "0-1", "0")
	driver.add("reclaim/child", childIdentity, "0-1", "0")
	driver.writeHook = func(driver *fakeHierarchyDriver, write fakeHierarchyWrite) error {
		if len(driver.writes) == 0 {
			driver.bumpIdentity("reclaim")
		}
		return nil
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["reclaim"] = machine.NewCPUSet(0, 1)
	cg.cpus["reclaim/child"] = machine.NewCPUSet(0, 1)
	provider := &coordinatorSnapshotTestCgroup{topologyFakeCgroup: cg, driver: driver}

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     provider,
		CPUDetails: machine.CPUDetails{0: {}, 1: {}},
		Budget:     ConvergenceBudget{MaxRounds: 1},
		RequiredIdentityByRel: map[string]CgroupIdentity{
			"reclaim": rootIdentity,
		},
	})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("Converge() error = %v, want ErrCoordinatorPlanStale", err)
	}
	if errors.Is(err, ErrRoundBudgetExceeded) {
		t.Fatalf("preflight identity stale was masked by MaxRounds: %v", err)
	}
	for _, write := range driver.writes {
		if write.rel == "reclaim" {
			t.Fatalf("replanned write reached replaced required rel: %#v", driver.writes)
		}
	}
}

func TestTopologyCoordinatorV1DynamicDescendantWithoutAllocationConvergesAcrossHandoffRounds(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(0), Mems: "0", TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.rejectEmptyCPUs = true
	cg.enforceParentContainsTarget = true
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.cpus["primary/child"] = machine.NewCPUSet(1)
	cg.children["primary"] = []string{"child"}

	input := CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: machine.CPUDetails{0: {}, 1: {}},
	}
	res, err := (TopologyCoordinator{}).Converge(context.Background(), input)
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !res.Converged || res.State != ConvergenceStateConverged {
		t.Fatalf("result = %+v, want converged", res)
	}
	if got := len(res.Rounds); got < 2 {
		t.Fatalf("rounds = %d, want expand handoff then next-round drain; writes=%#v", got, cg.writes)
	}
	if got, want := cg.cpus["primary"], machine.NewCPUSet(0); !got.Equals(want) {
		t.Fatalf("final primary cpus = %s, want %s", got.String(), want.String())
	}
	if got, want := cg.cpus["primary/child"], machine.NewCPUSet(0); !got.Equals(want) {
		t.Fatalf("final dynamic child cpus = %s, want %s", got.String(), want.String())
	}
	if len(cg.writes) < 3 {
		t.Fatalf("writes = %#v, want child expand followed by child and parent drain", cg.writes)
	}
	if first := cg.writes[0]; first.rel != "primary/child" || first.cpus != "0-1" || first.writeEmptyCPUs {
		t.Fatalf("first write = %#v, want non-empty same-domain parent-closure expand", first)
	}
	for i, write := range cg.writes {
		if write.writeEmptyCPUs {
			t.Fatalf("write[%d] = %#v, v1 normal convergence must not emit empty cpuset", i, write)
		}
	}

	writesAfterFirstConverge := len(cg.writes)
	res, err = (TopologyCoordinator{}).Converge(context.Background(), input)
	if err != nil {
		t.Fatalf("second stable Converge: %v", err)
	}
	if !res.Converged || res.State != ConvergenceStateConverged {
		t.Fatalf("second stable result = %+v, want converged", res)
	}
	if got := len(cg.writes); got != writesAfterFirstConverge {
		t.Fatalf("second stable Converge writes = %#v, want no writes after initial %d", cg.writes[writesAfterFirstConverge:], writesAfterFirstConverge)
	}
}

func TestTopologyCoordinatorV1ExplicitLeafDisjointReplacementNeverWritesEmpty(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
		CPUs: machine.NewCPUSet(0), Mems: "0", TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.rejectEmptyCPUs = true
	cg.enforceParentContainsTarget = true
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.cpus["primary/child"] = machine.NewCPUSet(1)
	cg.children["primary"] = []string{"child"}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:                 dag,
		Cgroup:              cg,
		CPUDetails:          machine.CPUDetails{0: {}, 1: {}},
		ExpectedCPUSetByRel: map[string]machine.CPUSet{"primary/child": machine.NewCPUSet(0)},
	})
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if !res.Converged || !cg.cpus["primary/child"].Equals(machine.NewCPUSet(0)) {
		t.Fatalf("result=%+v child=%s, want converged explicit replacement", res, cg.cpus["primary/child"].String())
	}
	for i, write := range cg.writes {
		if write.writeEmptyCPUs {
			t.Fatalf("write[%d] = %#v, explicit v1 replacement must not write empty", i, write)
		}
	}
}

func TestTopologyCoordinatorConvergesOpposingCPUAndMemsDirectionsAcrossPhases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		currentCPUs string
		targetCPUs  machine.CPUSet
		currentMems string
		targetMems  string
		wantWrites  []fakeHierarchyWrite
	}{
		{
			name:        "CPU shrink and mems grow",
			currentCPUs: "0-1", targetCPUs: machine.NewCPUSet(0),
			currentMems: "0", targetMems: "0-1",
			wantWrites: []fakeHierarchyWrite{
				{rel: "primary", cpus: machine.NewCPUSet(0), mems: "0"},
				{rel: "primary", cpus: machine.NewCPUSet(0), mems: "0-1"},
			},
		},
		{
			name:        "mems shrink and CPU grow",
			currentCPUs: "0", targetCPUs: machine.NewCPUSet(0, 1),
			currentMems: "0-1", targetMems: "1",
			wantWrites: []fakeHierarchyWrite{
				{rel: "primary", cpus: machine.NewCPUSet(0), mems: "1"},
				{rel: "primary", cpus: machine.NewCPUSet(0, 1), mems: "1"},
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dag, err := BuildDAG([]NodeSpec{{
				Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
				CPUs: tc.targetCPUs, Mems: tc.targetMems, TrustAnchor: true,
			}})
			if err != nil {
				t.Fatalf("BuildDAG: %v", err)
			}
			driver := newFakeHierarchyDriver()
			driver.allowUnwitnessedExpansion = true
			driver.add("primary", CgroupIdentity{Device: 1, Inode: 1}, tc.currentCPUs, tc.currentMems)
			cgroup := &coordinatorSnapshotTestCgroup{
				topologyFakeCgroup: newTopologyFakeCgroup(),
				driver:             driver,
			}

			result, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
				DAG: dag, Cgroup: cgroup,
				CPUDetails: machine.CPUDetails{0: {}, 1: {}},
			})
			if err != nil {
				t.Fatalf("Converge: %v; result=%+v", err, result)
			}
			if !result.Converged || result.State != ConvergenceStateConverged {
				t.Fatalf("result=%+v, want converged", result)
			}
			if len(driver.writes) != len(tc.wantWrites) {
				t.Fatalf("writes=%+v, want %+v", driver.writes, tc.wantWrites)
			}
			for i, want := range tc.wantWrites {
				got := driver.writes[i]
				if got.rel != want.rel || !got.cpus.Equals(want.cpus) || got.mems != want.mems {
					t.Fatalf("write[%d]=%+v, want %+v", i, got, want)
				}
			}
		})
	}
}

func TestTopologyCoordinatorBlocksStructuralV1DeadlockBeforeFirstWrite(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary, CPUs: machine.NewCPUSet(1), TrustAnchor: true},
		{Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim, CPUs: machine.NewCPUSet(0), TrustAnchor: true},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.rejectEmptyCPUs = true
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.cpus["reclaim"] = machine.NewCPUSet(1)

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG: dag, Cgroup: cg,
		CPUDetails: machine.CPUDetails{0: {}, 1: {}, 2: {}},
	})
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if res.State != ConvergenceStateBlocked || res.Converged {
		t.Fatalf("result = %+v, want blocked non-converged", res)
	}
	if len(res.Rounds) != 1 || res.Rounds[0].Status != RoundStatusBlocked {
		t.Fatalf("rounds = %+v, want one blocked round", res.Rounds)
	}
	var structural *deadlock.StructuralV1NonEmptyDeadlock
	if !errors.As(res.Rounds[0].Blocker, &structural) {
		t.Fatalf("blocker = %v, want deadlock.StructuralV1NonEmptyDeadlock", res.Rounds[0].Blocker)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("writes = %#v, want no writes before structural classification", cg.writes)
	}
}

func TestRoundOutcomeProgressIncludesVerifiedGrowWhenFinalSnapshotIsStale(t *testing.T) {
	t.Parallel()
	outcome := RoundOutcome{
		Status:   RoundStatusStale,
		Snapshot: &CompleteSnapshot{},
		Journal: []AppliedPlanOperation{{
			Direction: WriteGrow,
			Target:    CPUSetTarget{CPUs: machine.NewCPUSet(0, 1)},
			Observed:  CPUSetTarget{CPUs: machine.NewCPUSet(0, 1)},
		}},
	}
	if !roundOutcomeMadeNetProgress(outcome) {
		t.Fatal("verified grow must count as progress even when a later final snapshot is stale")
	}
}

func TestTopologyCoordinatorPartiallyDrainsSMTSiblingHeldByReclaimDescendants(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{
			Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
			CPUs: machine.NewCPUSet(0, 1, 2, 3, 4, 5), Mems: "0", TrustAnchor: true,
		},
		{
			Rel: "reclaim", Domain: DomainReclaim, Role: TopoNodeRoleReclaim,
			CPUs: machine.NewCPUSet(6, 7), Mems: "0", TrustAnchor: true,
		},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.rejectEmptyCPUs = true
	cg.enforceParentContainsTarget = true
	cg.cpus["primary"] = machine.NewCPUSet(0, 2, 3)
	cg.cpus["reclaim"] = machine.NewCPUSet(1, 4, 5, 6, 7)
	cg.cpus["reclaim/pod-a"] = machine.NewCPUSet(1, 4, 5, 6, 7)
	cg.cpus["reclaim/pod-b"] = machine.NewCPUSet(1, 4, 5, 6, 7)
	cg.children["reclaim"] = []string{"pod-a", "pod-b"}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:    dag,
		Cgroup: cg,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, CoreID: 0}, 1: {NUMANodeID: 0, CoreID: 0},
			2: {NUMANodeID: 0, CoreID: 1}, 3: {NUMANodeID: 0, CoreID: 1},
			4: {NUMANodeID: 0, CoreID: 2}, 5: {NUMANodeID: 0, CoreID: 2},
			6: {NUMANodeID: 0, CoreID: 3}, 7: {NUMANodeID: 0, CoreID: 3},
		},
		DrainSelection: DrainSelectionPolicy{
			MaxCPUsDrainRatio:         0.25,
			RequirePairedSwapProgress: true,
		},
	})
	if err != nil {
		t.Fatalf("Converge: %v; state=%s rounds=%+v journal=%+v", err, res.State, res.Rounds, res.Journal)
	}
	if !res.Converged || res.State != ConvergenceStateConverged {
		t.Fatalf("result state=%s converged=%t, want converged; rounds=%+v journal=%+v",
			res.State, res.Converged, res.Rounds, res.Journal)
	}
	if got := len(res.Rounds); got < 2 || got > 6 {
		t.Fatalf("rounds = %d, want finite multi-round convergence in [2,6]; rounds=%+v journal=%+v",
			got, res.Rounds, res.Journal)
	}
	for i, round := range res.Rounds {
		if round.Status == RoundStatusBlocked {
			t.Fatalf("round[%d] unexpectedly blocked: %+v", i, round)
		}
	}
	drained := machine.NewCPUSet()
	for _, round := range res.Rounds {
		for _, witness := range round.Witnesses {
			if witness.Source == DomainReclaim && witness.Destination == DomainPrimary {
				drained = drained.Union(witness.CPUs)
			}
		}
	}
	if want := machine.NewCPUSet(1, 4, 5); !drained.Equals(want) {
		t.Fatalf("reclaim-to-primary drain witnesses = %s, want requested CPUs %s including only sibling 1 from core 0; rounds=%+v",
			drained.String(), want.String(), res.Rounds)
	}
	for rel, want := range map[string]machine.CPUSet{
		"primary":       machine.NewCPUSet(0, 1, 2, 3, 4, 5),
		"reclaim":       machine.NewCPUSet(6, 7),
		"reclaim/pod-a": machine.NewCPUSet(6, 7),
		"reclaim/pod-b": machine.NewCPUSet(6, 7),
	} {
		if got := cg.cpus[rel]; !got.Equals(want) {
			t.Fatalf("final %s cpus = %s, want %s; writes=%#v", rel, got.String(), want.String(), cg.writes)
		}
	}
}

func TestTopologyCoordinatorV1StillRejectsConfiguredEmptyTargets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		spec     NodeSpec
		childRel string
		expected map[string]machine.CPUSet
	}{
		{
			name: "explicit empty dynamic allocation",
			spec: NodeSpec{
				Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
				CPUs: machine.NewCPUSet(0), TrustAnchor: true,
			},
			childRel: "primary/child",
			expected: map[string]machine.CPUSet{"primary/child": machine.NewCPUSet()},
		},
		{
			name: "controlled empty node",
			spec: NodeSpec{
				Rel: "primary", Domain: DomainPrimary, Role: TopoNodeRolePrimary,
				CPUs: machine.NewCPUSet(), TrustAnchor: true,
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dag, err := BuildDAG([]NodeSpec{tc.spec})
			if err != nil {
				t.Fatalf("BuildDAG: %v", err)
			}
			cg := newTopologyFakeCgroup()
			cg.rejectEmptyCPUs = true
			cg.cpus["primary"] = machine.NewCPUSet(0, 1)
			if tc.childRel != "" {
				cg.cpus[tc.childRel] = machine.NewCPUSet(1)
				cg.children["primary"] = []string{"child"}
			}

			_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
				DAG:                 dag,
				Cgroup:              cg,
				CPUDetails:          machine.CPUDetails{0: {}, 1: {}},
				ExpectedCPUSetByRel: tc.expected,
			})
			if !errors.Is(err, ErrEmptyCPUSetUnsupported) {
				t.Fatalf("Converge error = %v, want %v", err, ErrEmptyCPUSetUnsupported)
			}
		})
	}
}

func TestTopologyCoordinatorRevalidatesFreshSnapshotBeforePublishingUnderModeGate(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}

	t.Run("snapshot drift returns to owner without internal retry", func(t *testing.T) {
		t.Parallel()
		gate := NewModeGate()
		cg := newTopologyFakeCgroup()
		cg.cpus["primary"] = machine.NewCPUSet(0, 1)
		cg.afterSnapshotRootRead = func(reads int) {
			if reads == 4 {
				cg.cpus["primary"] = machine.NewCPUSet(0)
			}
		}
		published := 0
		res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
			DAG:        dag,
			Cgroup:     cg,
			Mode:       NormalModeGuardWithGate(gate),
			CPUDetails: machine.CPUDetails{0: {}, 1: {}},
			PublishFinalSnapshot: func(*CompleteSnapshot) error {
				published++
				return nil
			},
		})
		if !errors.Is(err, ErrCoordinatorPlanStale) {
			t.Fatalf("Converge error = %v, want final snapshot stale", err)
		}
		if res.Converged || res.Published || published != 0 ||
			res.ReplanDisposition != ReplanSafeNoPhysicalWrites {
			t.Fatalf("result=%+v published=%d, want owner-visible zero-write stale", res, published)
		}
		if len(res.Rounds) != 1 {
			t.Fatalf("rounds = %+v, want one stale publish round", res.Rounds)
		}
		stale := res.Rounds[0]
		var staleErr *PlanStaleError
		if stale.Status != RoundStatusStale || !errors.As(stale.Blocker, &staleErr) ||
			staleErr.Resource != "final_snapshot" {
			t.Fatalf("first round = %+v, want final_snapshot stale outcome", stale)
		}
		if stale.Snapshot == nil || !stale.Snapshot.Entries["primary"].CPUs.Equals(machine.NewCPUSet(0)) {
			t.Fatalf("stale snapshot=%v, want fresh changed snapshot retained for replan", stale.Snapshot)
		}
		if res.FinalSnapshot != nil || res.FinalSnapshotCurrent {
			t.Fatalf("final snapshot=%v current=%t, want zero-write disposition to force owner rescan",
				res.FinalSnapshot, res.FinalSnapshotCurrent)
		}
	})

	t.Run("controlled snapshot keeps changing across two publish attempts", func(t *testing.T) {
		t.Parallel()
		gate := NewModeGate()
		cg := newTopologyFakeCgroup()
		cg.cpus["primary"] = machine.NewCPUSet(0, 1)
		cg.afterSnapshotRootRead = func(reads int) {
			switch reads {
			case 4:
				cg.cpus["primary"] = machine.NewCPUSet(0)
			case 10:
				cg.cpus["primary"] = machine.NewCPUSet(0)
			}
		}
		published := 0
		res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
			DAG:        dag,
			Cgroup:     cg,
			Mode:       NormalModeGuardWithGate(gate),
			CPUDetails: machine.CPUDetails{0: {}, 1: {}},
			PublishFinalSnapshot: func(*CompleteSnapshot) error {
				published++
				return nil
			},
		})
		if !errors.Is(err, ErrCoordinatorPlanStale) {
			t.Fatalf("Converge error=%v, want owner-visible stale; result=%+v", err, res)
		}
		if res.Converged || res.State != ConvergenceStateNonConverged {
			t.Fatalf("result=%+v, want terminal coordinator attempt", res)
		}
		if published != 0 || len(res.Rounds) != 1 {
			t.Fatalf("published=%d rounds=%+v, want one stale round and no publish", published, res.Rounds)
		}
		if res.ReplanDisposition != ReplanSafeNoPhysicalWrites {
			t.Fatalf("disposition=%v, want zero-write owner replan", res.ReplanDisposition)
		}
	})

	t.Run("unrelated dynamic leaf continuously churns", func(t *testing.T) {
		t.Parallel()
		gate := NewModeGate()
		cg := newTopologyFakeCgroup()
		cg.cpus["primary"] = machine.NewCPUSet(0, 1)
		cg.afterSnapshotRootRead = func(reads int) {
			delete(cg.cpus, "primary/leaf-a")
			delete(cg.cpus, "primary/leaf-b")
			if reads%2 == 0 {
				cg.children["primary"] = []string{"leaf-b"}
				cg.cpus["primary/leaf-b"] = machine.NewCPUSet(0, 1)
			} else {
				cg.children["primary"] = []string{"leaf-a"}
				cg.cpus["primary/leaf-a"] = machine.NewCPUSet(0, 1)
			}
		}
		published := 0
		var publishedSnapshot *CompleteSnapshot
		res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
			DAG:        dag,
			Cgroup:     cg,
			Mode:       NormalModeGuardWithGate(gate),
			CPUDetails: machine.CPUDetails{0: {}, 1: {}},
			PublishFinalSnapshot: func(snapshot *CompleteSnapshot) error {
				published++
				publishedSnapshot = snapshot
				return nil
			},
		})
		if err != nil {
			t.Fatalf("Converge: %v", err)
		}
		if !res.Converged || !res.FinalSnapshotCurrent || published != 1 {
			t.Fatalf("result=%+v published=%d, want unrelated churn ignored and one fresh snapshot published", res, published)
		}
		if res.FinalSnapshot == nil || publishedSnapshot == nil || res.FinalSnapshot == publishedSnapshot {
			t.Fatalf("FinalSnapshot=%p published=%p, want independent copies of the verified snapshot",
				res.FinalSnapshot, publishedSnapshot)
		}
		if _, hasLeafB := res.FinalSnapshot.Entries["primary/leaf-b"]; !hasLeafB {
			t.Fatalf("FinalSnapshot entries=%v, want fresh complete snapshot containing latest unrelated leaf", res.FinalSnapshot.Entries)
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				publishedSnapshot.OwnershipByRel = map[string]machine.CPUSet{
					"publication": machine.NewCPUSet(i),
				}
			}
		}()
		go func() {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				res.FinalSnapshot.OwnershipByRel = map[string]machine.CPUSet{
					"result": machine.NewCPUSet(i),
				}
			}
		}()
		wg.Wait()
		require.NotContains(t, res.FinalSnapshot.OwnershipByRel, "publication")
		require.NotContains(t, publishedSnapshot.OwnershipByRel, "result")
	})

	t.Run("expected materialized leaf keeps changing across publish attempts", func(t *testing.T) {
		t.Parallel()
		gate := NewModeGate()
		const leaf = "primary/expected"
		cg := newTopologyFakeCgroup()
		cg.cpus["primary"] = machine.NewCPUSet(0, 1)
		cg.cpus[leaf] = machine.NewCPUSet(0)
		cg.children["primary"] = []string{"expected"}
		cg.afterSnapshotRootRead = func(reads int) {
			if reads == 4 || reads == 10 {
				cg.cpus[leaf] = machine.NewCPUSet(1)
			}
		}
		published := 0
		res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
			DAG:                 dag,
			Cgroup:              cg,
			Mode:                NormalModeGuardWithGate(gate),
			CPUDetails:          machine.CPUDetails{0: {}, 1: {}},
			ExpectedCPUSetByRel: map[string]machine.CPUSet{leaf: machine.NewCPUSet(0)},
			PublishFinalSnapshot: func(*CompleteSnapshot) error {
				published++
				return nil
			},
		})
		if !errors.Is(err, ErrCoordinatorPlanStale) {
			t.Fatalf("Converge error=%v, want owner-visible stale; result=%+v", err, res)
		}
		if res.Converged || res.State != ConvergenceStateNonConverged || published != 0 {
			t.Fatalf("result=%+v published=%d, want one coordinator stale attempt", res, published)
		}
		if len(res.Rounds) != 1 || res.ReplanDisposition != ReplanSafeNoPhysicalWrites {
			t.Fatalf("rounds = %+v disposition=%v, want one zero-write stale owner replan",
				res.Rounds, res.ReplanDisposition)
		}
	})

	t.Run("stable snapshot", func(t *testing.T) {
		t.Parallel()
		gate := NewModeGate()
		cg := newTopologyFakeCgroup()
		cg.cpus["primary"] = machine.NewCPUSet(0, 1)
		published := 0
		res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
			DAG:        dag,
			Cgroup:     cg,
			Mode:       NormalModeGuardWithGate(gate),
			CPUDetails: machine.CPUDetails{0: {}, 1: {}},
			PublishFinalSnapshot: func(snapshot *CompleteSnapshot) error {
				published++
				if snapshot == nil {
					t.Fatal("published nil snapshot")
				}
				_, enterErr := ResetModeGuardWithGate(gate).TryEnter()
				var busy *CoordinatorBusyError
				if !errors.As(enterErr, &busy) {
					t.Fatalf("publish callback mode gate error = %T %v, want busy", enterErr, enterErr)
				}
				return nil
			},
		})
		if err != nil {
			t.Fatalf("Converge: %v", err)
		}
		if !res.Converged || published != 1 {
			t.Fatalf("result=%+v published=%d, want one guarded publication", res, published)
		}
	})
}

func TestSafeWriterAllowsSafeChildChurnDuringShrink(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*fakeHierarchyDriver){
		"addition": func(driver *fakeHierarchyDriver) {
			driver.add("root/new", CgroupIdentity{Device: 1, Inode: 3}, "1", "0")
		},
		"deletion": func(driver *fakeHierarchyDriver) {
			delete(driver.nodes, "root/old")
		},
		"identity churn": func(driver *fakeHierarchyDriver) {
			driver.bumpIdentity("root/old")
		},
	}
	for name, churn := range tests {
		name := name
		churn := churn
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			driver := newFakeHierarchyDriver()
			rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
			oldChildIdentity := CgroupIdentity{Device: 1, Inode: 2}
			driver.add("root", rootIdentity, "0-3", "0")
			driver.add("root/old", oldChildIdentity, "0", "0")
			plan := PhasePlan{
				ConvergenceID: "safe-child-churn",
				Kind:          PhaseDrain,
				Base: planSnapshot(map[string]EntryState{
					"root":     {Rel: "root", Identity: rootIdentity, CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
					"root/old": {Rel: "root/old", Identity: oldChildIdentity, CPUs: machine.NewCPUSet(0), Mems: "0"},
				}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1, 2, 3)}),
				Operations: []PlanOperation{{
					Rel: "root", ExpectedIdentity: rootIdentity,
					ExpectedChildren:   ChildrenFingerprint([]ChildRef{{Name: "old", Identity: oldChildIdentity}}),
					ExpectedChildUnion: machine.NewCPUSet(0),
					ExpectedCurrent:    CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
					Target:             CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2), Mems: "0"},
					Direction:          WriteShrink,
				}},
			}
			plan.PlanID = canonicalExecutionPlanID(plan)
			plan.Operations[0].PlanID = plan.PlanID
			churn(driver)

			if err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
				execute(context.Background(), plan); err != nil {
				t.Fatalf("safe child %s rejected: %v", name, err)
			}
			if got := driver.nodes["root"].cpus.String(); got != "0-2" {
				t.Fatalf("root cpus after safe child %s = %s, want 0-2", name, got)
			}
		})
	}
}

func TestSafeWriterAllowsChildAdditionAndDeletionDuringGrow(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*fakeHierarchyDriver){
		"addition": func(driver *fakeHierarchyDriver) {
			driver.add("root/new", CgroupIdentity{Device: 1, Inode: 3}, "0", "0")
		},
		"deletion": func(driver *fakeHierarchyDriver) {
			delete(driver.nodes, "root/old")
		},
	}
	for name, churn := range tests {
		name := name
		churn := churn
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			driver := newFakeHierarchyDriver()
			driver.allowUnwitnessedExpansion = true
			rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
			oldChildIdentity := CgroupIdentity{Device: 1, Inode: 2}
			driver.add("root", rootIdentity, "0", "0")
			driver.add("root/old", oldChildIdentity, "0", "0")
			plan := testGrowPlan("grow-child-"+name, []PlanOperation{{
				Rel: "root", ExpectedIdentity: rootIdentity,
				ExpectedChildren: ChildrenFingerprint([]ChildRef{{Name: "old", Identity: oldChildIdentity}}),
				ExpectedCurrent:  CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
				Target:           CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
				Direction:        WriteGrow,
			}}, map[string]EntryState{
				"root":     {Rel: "root", Identity: rootIdentity, CPUs: machine.NewCPUSet(0), Mems: "0"},
				"root/old": {Rel: "root/old", Identity: oldChildIdentity, CPUs: machine.NewCPUSet(0), Mems: "0"},
			})
			churn(driver)

			if err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
				execute(context.Background(), plan); err != nil {
				t.Fatalf("grow after child %s: %v", name, err)
			}
			if got := driver.nodes["root"].cpus.String(); got != "0-1" {
				t.Fatalf("root cpus after child %s = %s, want 0-1", name, got)
			}
		})
	}
}

func TestSafeWriterGrowStillRejectsChangedOperationIdentity(t *testing.T) {
	t.Parallel()

	driver := newFakeHierarchyDriver()
	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	driver.add("root", rootIdentity, "0", "0")
	driver.add("root/child", childIdentity, "0", "0")
	plan := testGrowPlan("grow-operation-identity", []PlanOperation{{
		Rel: "root", ExpectedIdentity: rootIdentity,
		ExpectedChildren: ChildrenFingerprint([]ChildRef{{Name: "child", Identity: childIdentity}}),
		ExpectedCurrent:  CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
		Target:           CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		Direction:        WriteGrow,
	}}, map[string]EntryState{
		"root":       {Rel: "root", Identity: rootIdentity, CPUs: machine.NewCPUSet(0), Mems: "0"},
		"root/child": {Rel: "root/child", Identity: childIdentity, CPUs: machine.NewCPUSet(0), Mems: "0"},
	})
	plan.Operations[0].ExpectedChildren = ChildrenFingerprint(nil)

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	if err == nil || !strings.Contains(err.Error(), "canonical plan id") {
		t.Fatalf("execute error = %v, want changed operation identity rejection", err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("changed operation identity allowed writes: %#v", driver.writes)
	}
}

func TestSafeWriterGrowPrecheckRejectsTargetThatIsNotCurrentSuperset(t *testing.T) {
	t.Parallel()

	driver := newFakeHierarchyDriver()
	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	driver.add("root", rootIdentity, "0-1", "0")
	plan := testGrowPlan("grow-target-not-superset", []PlanOperation{{
		Rel: "root", ExpectedIdentity: rootIdentity,
		ExpectedChildren: ChildrenFingerprint(nil),
		ExpectedCurrent:  CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		Target:           CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
		Direction:        WriteGrow,
	}}, map[string]EntryState{
		"root": {Rel: "root", Identity: rootIdentity, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
	})

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) || stale.Resource != "cpuset.cpus" {
		t.Fatalf("execute error = %T %v, want cpuset.cpus plan stale", err, err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("non-superset grow target allowed writes: %#v", driver.writes)
	}
}

func TestSafeWriterClassifiesDisappearingOperationAsPlanStale(t *testing.T) {
	t.Parallel()

	tests := map[string]struct {
		arrange    func(*fakeHierarchyDriver)
		wantWrites int
	}{
		"before precheck": {
			arrange: func(driver *fakeHierarchyDriver) {
				delete(driver.nodes, "root/leaf")
			},
			wantWrites: 0,
		},
		"after write": {
			arrange: func(driver *fakeHierarchyDriver) {
				reads := 0
				driver.beforeCall = func(op HierarchyOperation, rel string) error {
					if op == HierarchyOperationRead && rel == "root/leaf" {
						reads++
						if reads == 2 {
							delete(driver.nodes, rel)
						}
					}
					return nil
				}
			},
			wantWrites: 1,
		},
	}
	for name, tc := range tests {
		name := name
		tc := tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			driver := newFakeHierarchyDriver()
			driver.allowUnwitnessedExpansion = true
			rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
			leafIdentity := CgroupIdentity{Device: 1, Inode: 2}
			driver.add("root", rootIdentity, "0-1", "0")
			driver.add("root/leaf", leafIdentity, "0", "0")
			plan := testGrowPlan("disappearing-operation-"+name, []PlanOperation{{
				Rel: "root/leaf", ExpectedIdentity: leafIdentity,
				ParentRel: "root", ExpectedParentIdentity: rootIdentity,
				ExpectedChildren: ChildrenFingerprint(nil),
				ExpectedCurrent:  CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
				Target:           CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
				Direction:        WriteGrow,
			}}, map[string]EntryState{
				"root":      {Rel: "root", Identity: rootIdentity, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
				"root/leaf": {Rel: "root/leaf", Identity: leafIdentity, CPUs: machine.NewCPUSet(0), Mems: "0"},
			})
			tc.arrange(driver)

			err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
				execute(context.Background(), plan)
			var stale *PlanStaleError
			if !errors.As(err, &stale) || stale.Rel != "root/leaf" ||
				stale.Resource != "hierarchy" || !errors.Is(stale.Err, syscall.ENOENT) {
				t.Fatalf("execute error = %T %v, want hierarchy PlanStaleError wrapping ENOENT", err, err)
			}
			if got := len(driver.writes); got != tc.wantWrites {
				t.Fatalf("writes = %d, want %d for %s race", got, tc.wantWrites, name)
			}
		})
	}
}

func TestSafeWriterClassifiesLiveParentContainmentDriftAsPlanStale(t *testing.T) {
	t.Parallel()

	driver := newFakeHierarchyDriver()
	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	leafIdentity := CgroupIdentity{Device: 1, Inode: 2}
	driver.add("root", rootIdentity, "0", "0")
	driver.add("root/leaf", leafIdentity, "0", "0")
	plan := testGrowPlan("parent-containment-drift", []PlanOperation{{
		Rel: "root/leaf", ExpectedIdentity: leafIdentity,
		ParentRel: "root", ExpectedParentIdentity: rootIdentity,
		ExpectedChildren: ChildrenFingerprint(nil),
		ExpectedCurrent:  CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
		Target:           CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		Direction:        WriteGrow,
	}}, map[string]EntryState{
		"root":      {Rel: "root", Identity: rootIdentity, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		"root/leaf": {Rel: "root/leaf", Identity: leafIdentity, CPUs: machine.NewCPUSet(0), Mems: "0"},
	})

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) || stale.Rel != "root/leaf" ||
		stale.Resource != "parent_cpuset.cpus" || stale.Current != "0" || stale.Target != "0-1" {
		t.Fatalf("execute error = %T %v, want parent cpuset PlanStaleError", err, err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("parent containment drift allowed writes: %#v", driver.writes)
	}
}

func TestSafeWriterDoesNotClassifyPermanentReadErrorAsPlanStale(t *testing.T) {
	t.Parallel()

	driver := newFakeHierarchyDriver()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	driver.add("root", identity, "0", "0")
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "root" {
			return syscall.EACCES
		}
		return nil
	}
	plan := testGrowPlan("permanent-read-error", []PlanOperation{{
		Rel: "root", ExpectedIdentity: identity, ExpectedChildren: ChildrenFingerprint(nil),
		ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
		Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		Direction:       WriteGrow,
	}}, map[string]EntryState{
		"root": {Rel: "root", Identity: identity, CPUs: machine.NewCPUSet(0), Mems: "0"},
	})

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.Is(err, syscall.EACCES) || errors.As(err, &stale) {
		t.Fatalf("execute error = %T %v, want unchanged EACCES without PlanStaleError", err, err)
	}
}

func TestSafeWriterRejectsUnsafeChildAdditionDuringShrinkAsStale(t *testing.T) {
	t.Parallel()

	driver := newFakeHierarchyDriver()
	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	driver.add("root", rootIdentity, "0-3", "0")
	plan := PhasePlan{
		ConvergenceID: "unsafe-child-addition",
		Kind:          PhaseDrain,
		Base: planSnapshot(map[string]EntryState{
			"root": {Rel: "root", Identity: rootIdentity, CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
		}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1, 2, 3)}),
		Operations: []PlanOperation{{
			Rel: "root", ExpectedIdentity: rootIdentity,
			ExpectedChildren:   ChildrenFingerprint(nil),
			ExpectedChildUnion: machine.NewCPUSet(),
			ExpectedCurrent:    CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
			Target:             CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2), Mems: "0"},
			Direction:          WriteShrink,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	driver.add("root/unsafe", CgroupIdentity{Device: 1, Inode: 2}, "3", "0")

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) || stale.Resource != "child_union" {
		t.Fatalf("writer error = %T %v, want child_union replan stale", err, err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("unsafe child addition allowed writes: %#v", driver.writes)
	}
}

func TestSafeWriterClassifiesMemsWriteEBUSYAsPlanStale(t *testing.T) {
	t.Parallel()

	driver := newFakeHierarchyDriver()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	driver.add("root", identity, "0", "0")
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationWriteMems && rel == "root" {
			return syscall.EBUSY
		}
		return nil
	}
	plan := PhasePlan{
		ConvergenceID: "mems-ebusy",
		Kind:          PhaseExpand,
		Base: planSnapshot(map[string]EntryState{
			"root": {Rel: "root", Identity: identity, CPUs: machine.NewCPUSet(0), Mems: "0"},
		}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)}),
		Operations: []PlanOperation{{
			Rel: "root", ExpectedIdentity: identity, ExpectedChildren: ChildrenFingerprint(nil),
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0-1"},
			Direction:       WriteGrow, OwnsMems: true, WriteMems: true,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) {
		t.Fatalf("execute error=%T %v, want PlanStaleError", err, err)
	}
	if stale.Rel != "root" || stale.Direction != WriteGrow || stale.Resource != "cpuset.mems" ||
		stale.Current != "0" || stale.Target != "0-1" || !errors.Is(stale.Err, syscall.EBUSY) ||
		!stale.ReplanRequired() {
		t.Fatalf("stale=%+v, want root/grow/cpuset.mems current=0 target=0-1 EBUSY replan", stale)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("failed mems write mutated hierarchy: %#v", driver.writes)
	}
}

func TestSafeWriterRollsBackMemsWhenCPUWriteIsStale(t *testing.T) {
	t.Parallel()

	driver := newFakeHierarchyDriver()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	driver.add("root", identity, "0", "0")
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationWriteCPUs && rel == "root" {
			return syscall.EBUSY
		}
		return nil
	}
	plan := PhasePlan{
		ConvergenceID: "partial-mems",
		Kind:          PhaseExpand,
		Base: planSnapshot(map[string]EntryState{
			"root": {Rel: "root", Identity: identity, CPUs: machine.NewCPUSet(0), Mems: "0"},
		}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)}),
		Operations: []PlanOperation{{
			Rel: "root", ExpectedIdentity: identity, ExpectedChildren: ChildrenFingerprint(nil),
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0-1"},
			Direction:       WriteGrow, OwnsMems: true, WriteMems: true,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	res := &ConvergenceResult{}

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), res).
		execute(context.Background(), plan)
	var stale *PlanStaleError
	if !errors.As(err, &stale) {
		t.Fatalf("execute error=%T %v, want PlanStaleError", err, err)
	}
	if stale.Resource != "cpuset.cpus" || stale.Current != "0" || stale.Target != "0-1" ||
		!errors.Is(stale.Err, syscall.EBUSY) {
		t.Fatalf("stale=%+v, want fresh CPU observation after partial mems apply", stale)
	}
	if got := driver.nodes["root"].mems; got != "0" {
		t.Fatalf("live mems=%q, want rollback to 0", got)
	}
	if len(res.Journal) != 0 {
		t.Fatalf("journal=%+v, want no applied operation after rollback", res.Journal)
	}
	if res.Applied != 0 || res.Failed != 1 {
		t.Fatalf("result=%+v, want partial failure without fully applied count", res)
	}
}

func TestSafeWriterStableShrinkScanRetriesListReadDeletionAndRecreate(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*fakeHierarchyDriver){
		"deletion": func(driver *fakeHierarchyDriver) {
			delete(driver.nodes, "root/child")
		},
		"same-name recreate": func(driver *fakeHierarchyDriver) {
			driver.bumpIdentity("root/child")
		},
	}
	for name, churn := range tests {
		name := name
		churn := churn
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			driver := newFakeHierarchyDriver()
			rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
			childIdentity := CgroupIdentity{Device: 1, Inode: 2}
			driver.add("root", rootIdentity, "0-3", "0")
			driver.add("root/child", childIdentity, "0", "0")
			plan := testShrinkPlan("stable-scan-"+name, []PlanOperation{{
				Rel: "root", ExpectedIdentity: rootIdentity,
				ExpectedChildren:   ChildrenFingerprint([]ChildRef{{Name: "child", Identity: childIdentity}}),
				ExpectedChildUnion: machine.NewCPUSet(0),
				ExpectedCurrent:    CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
				Target:             CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2), Mems: "0"},
				Direction:          WriteShrink,
			}}, map[string]EntryState{
				"root":       {Rel: "root", Identity: rootIdentity, CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
				"root/child": {Rel: "root/child", Identity: childIdentity, CPUs: machine.NewCPUSet(0), Mems: "0"},
			})
			childReads := 0
			driver.beforeCall = func(op HierarchyOperation, rel string) error {
				if op == HierarchyOperationRead && rel == "root/child" {
					childReads++
					if childReads == 1 {
						churn(driver)
					}
				}
				return nil
			}

			if err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{
				MaxHierarchyIOOperations: 16,
			}), nil).execute(context.Background(), plan); err != nil {
				t.Fatalf("execute after %s race: %v", name, err)
			}
			if childReads < 1 || len(driver.writes) != 1 {
				t.Fatalf("%s race childReads=%d writes=%#v, want retried scan and one write", name, childReads, driver.writes)
			}
		})
	}
}

func TestSafeWriterNewShrinkChildBudgetFailureHappensBeforeAnyWrite(t *testing.T) {
	t.Parallel()

	driver := newFakeHierarchyDriver()
	firstIdentity := CgroupIdentity{Device: 1, Inode: 1}
	secondIdentity := CgroupIdentity{Device: 1, Inode: 2}
	driver.add("first", firstIdentity, "0-3", "0")
	driver.add("second", secondIdentity, "0-3", "0")
	driver.add("second/new", CgroupIdentity{Device: 1, Inode: 3}, "0", "0")
	plan := testShrinkPlan("new-child-budget", []PlanOperation{
		{
			Rel: "first", ExpectedIdentity: firstIdentity,
			ExpectedChildren: ChildrenFingerprint(nil), ExpectedChildUnion: machine.NewCPUSet(),
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2), Mems: "0"}, Direction: WriteShrink,
		},
		{
			Rel: "second", ExpectedIdentity: secondIdentity,
			ExpectedChildren: ChildrenFingerprint(nil), ExpectedChildUnion: machine.NewCPUSet(),
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2), Mems: "0"}, Direction: WriteShrink,
		},
	}, map[string]EntryState{
		"first":  {Rel: "first", Identity: firstIdentity, CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
		"second": {Rel: "second", Identity: secondIdentity, CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
	})
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 10})

	err := newSafeCPUSetWriter(driver, budget, nil).execute(context.Background(), plan)
	if !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("execute error = %v, want hierarchy I/O budget exceeded", err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("budget failure partially applied writes: %#v", driver.writes)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 5 {
		t.Fatalf("charged hierarchy I/O = %d, want exact stable scans list/list + list/read/list = 5", got)
	}
}

func TestSafeWriterPersistentShrinkChildChurnStopsAtSharedBudgetWithoutWrites(t *testing.T) {
	t.Parallel()

	driver := newFakeHierarchyDriver()
	rootIdentity := CgroupIdentity{Device: 1, Inode: 1}
	childIdentity := CgroupIdentity{Device: 1, Inode: 2}
	driver.add("root", rootIdentity, "0-3", "0")
	driver.add("root/child", childIdentity, "0", "0")
	plan := testShrinkPlan("persistent-child-churn", []PlanOperation{{
		Rel: "root", ExpectedIdentity: rootIdentity,
		ExpectedChildren:   ChildrenFingerprint([]ChildRef{{Name: "child", Identity: childIdentity}}),
		ExpectedChildUnion: machine.NewCPUSet(0),
		ExpectedCurrent:    CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
		Target:             CPUSetTarget{CPUs: machine.NewCPUSet(0, 1, 2), Mems: "0"},
		Direction:          WriteShrink,
	}}, map[string]EntryState{
		"root":       {Rel: "root", Identity: rootIdentity, CPUs: machine.NewCPUSet(0, 1, 2, 3), Mems: "0"},
		"root/child": {Rel: "root/child", Identity: childIdentity, CPUs: machine.NewCPUSet(0), Mems: "0"},
	})
	driver.beforeCall = func(op HierarchyOperation, rel string) error {
		if op == HierarchyOperationRead && rel == "root/child" {
			driver.bumpIdentity(rel)
		}
		return nil
	}
	budget := NewBudgetTracker(ConvergenceBudget{MaxHierarchyIOOperations: 5})

	err := newSafeCPUSetWriter(driver, budget, nil).execute(context.Background(), plan)
	if !errors.Is(err, ErrHierarchyIOOperationBudgetExceeded) {
		t.Fatalf("execute error = %v, want hierarchy I/O budget exceeded", err)
	}
	if len(driver.writes) != 0 {
		t.Fatalf("persistent churn allowed writes: %#v", driver.writes)
	}
	if got := budget.Usage().HierarchyIOOperations; got != 5 {
		t.Fatalf("charged hierarchy I/O = %d, want exhausted shared budget 5", got)
	}
}

func testShrinkPlan(convergenceID string, operations []PlanOperation, entries map[string]EntryState) PhasePlan {
	plan := PhasePlan{
		ConvergenceID: convergenceID,
		Kind:          PhaseDrain,
		Base:          planSnapshot(entries, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1, 2, 3)}),
		Operations:    operations,
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}
	return plan
}

func testGrowPlan(convergenceID string, operations []PlanOperation, entries map[string]EntryState) PhasePlan {
	plan := PhasePlan{
		ConvergenceID: convergenceID,
		Kind:          PhaseExpand,
		Base:          planSnapshot(entries, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)}),
		Operations:    operations,
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}
	return plan
}

func TestPublishRelevantSnapshotsEqual(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	identity := CgroupIdentity{Device: 1, Inode: 1}
	base := &CompleteSnapshot{Entries: map[string]EntryState{
		"primary":          {Rel: "primary", Identity: identity, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		"primary/expected": {Rel: "primary/expected", Identity: identity, CPUs: machine.NewCPUSet(0), Mems: "0"},
		"primary/dynamic":  {Rel: "primary/dynamic", Identity: identity, CPUs: machine.NewCPUSet(1), Mems: "0"},
	}}
	expected := map[string]machine.CPUSet{"primary/expected": machine.NewCPUSet(0)}

	tests := []struct {
		name  string
		alter func(map[string]EntryState)
		equal bool
	}{
		{
			name: "unrelated dynamic leaf may disappear",
			alter: func(entries map[string]EntryState) {
				delete(entries, "primary/dynamic")
			},
			equal: true,
		},
		{
			name: "controlled rel must exist",
			alter: func(entries map[string]EntryState) {
				delete(entries, "primary")
			},
		},
		{
			name: "controlled identity is relevant",
			alter: func(entries map[string]EntryState) {
				entry := entries["primary"]
				entry.Identity.Inode++
				entries["primary"] = entry
			},
		},
		{
			name: "controlled cpus are relevant",
			alter: func(entries map[string]EntryState) {
				entry := entries["primary"]
				entry.CPUs = machine.NewCPUSet(0)
				entries["primary"] = entry
			},
		},
		{
			name: "controlled mems are relevant",
			alter: func(entries map[string]EntryState) {
				entry := entries["primary"]
				entry.Mems = "1"
				entries["primary"] = entry
			},
		},
		{
			name: "configured cpu drift with stable effective state blocks publish",
			alter: func(entries map[string]EntryState) {
				entry := entries["primary"]
				entry.ConfiguredCPUs = machine.NewCPUSet(1)
				entries["primary"] = entry
			},
		},
		{
			name: "configured mem drift with stable effective state blocks publish",
			alter: func(entries map[string]EntryState) {
				entry := entries["primary"]
				entry.ConfiguredMems = "1"
				entries["primary"] = entry
			},
		},
		{
			name: "expected materialized leaf existence is relevant",
			alter: func(entries map[string]EntryState) {
				delete(entries, "primary/expected")
			},
		},
		{
			name: "expected materialized leaf identity is relevant",
			alter: func(entries map[string]EntryState) {
				entry := entries["primary/expected"]
				entry.Identity.Inode++
				entries["primary/expected"] = entry
			},
		},
		{
			name: "expected materialized leaf cpus are relevant",
			alter: func(entries map[string]EntryState) {
				entry := entries["primary/expected"]
				entry.CPUs = machine.NewCPUSet(1)
				entries["primary/expected"] = entry
			},
		},
		{
			name: "expected materialized leaf mems are relevant",
			alter: func(entries map[string]EntryState) {
				entry := entries["primary/expected"]
				entry.Mems = "1"
				entries["primary/expected"] = entry
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			entries := make(map[string]EntryState, len(base.Entries))
			for rel, entry := range base.Entries {
				entry.CPUs = entry.CPUs.Clone()
				entries[rel] = entry
			}
			tc.alter(entries)
			fresh := &CompleteSnapshot{Entries: entries}
			if got := publishRelevantSnapshotsEqual(dag, expected, base, fresh); got != tc.equal {
				t.Fatalf("publishRelevantSnapshotsEqual() = %t, want %t", got, tc.equal)
			}
		})
	}
}

func TestTopologyMemsAreVerifiedPostWriteAndInFinalReport(t *testing.T) {
	t.Parallel()

	t.Run("post write", func(t *testing.T) {
		t.Parallel()
		driver := newFakeHierarchyDriver()
		identity := CgroupIdentity{Device: 1, Inode: 1}
		driver.add("primary", identity, "0", "0")
		driver.beforeCall = func(operation HierarchyOperation, rel string) error {
			if operation == HierarchyOperationRead && rel == "primary" && len(driver.writes) == 1 {
				driver.nodes[rel].mems = "0"
			}
			return nil
		}
		plan := PhasePlan{
			ConvergenceID: "mems-post-write",
			Kind:          PhaseExpand,
			Base: planSnapshot(map[string]EntryState{
				"primary": {Rel: "primary", Identity: identity, CPUs: machine.NewCPUSet(0), Mems: "0"},
			}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0)}),
			Operations: []PlanOperation{{
				Rel: "primary", ExpectedIdentity: identity,
				ExpectedChildren: ChildrenFingerprint(nil),
				ExpectedCurrent:  CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
				Target:           CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0-1"},
				Direction:        WriteGrow,
				OwnsMems:         true,
				WriteMems:        true,
			}},
		}
		plan.PlanID = canonicalExecutionPlanID(plan)
		plan.Operations[0].PlanID = plan.PlanID
		err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).execute(context.Background(), plan)
		var stale *PlanStaleError
		if !errors.As(err, &stale) || stale.Resource != "post_write_cpuset.mems" {
			t.Fatalf("writer error = %T %v, want post-write mems stale error", err, err)
		}
	})

	t.Run("final report", func(t *testing.T) {
		t.Parallel()
		dag, err := BuildDAG([]NodeSpec{{
			Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0), Mems: "1",
		}})
		if err != nil {
			t.Fatalf("BuildDAG: %v", err)
		}
		snapshot := planSnapshot(map[string]EntryState{
			"primary": {Rel: "primary", CPUs: machine.NewCPUSet(0), Mems: "0"},
		}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0)})
		report, err := buildConvergenceReport(
			snapshot,
			dag,
			map[string]machine.CPUSet{"primary": machine.NewCPUSet(0)},
			map[string]string{"primary": "1"},
			map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0)},
			machine.NewCPUSet(0),
			cgroupV1Policy.capabilities(true),
			false,
		)
		if err != nil {
			t.Fatalf("buildConvergenceReport: %v", err)
		}
		if report.FullyConverged || len(report.NonConvergedTargets) != 1 {
			t.Fatalf("report = %+v, want one mems mismatch", report)
		}
		if got := report.NonConvergedTargets[0]; got.ObservedMems != "0" || got.TargetMems != "1" {
			t.Fatalf("mems mismatch = %+v, want observed 0 target 1", got)
		}
	})
}

func TestDrainRebaseAndFrontierRespectPlanOperationBudget(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(1, 2), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	identity := CgroupIdentity{Device: 1, Inode: 1}
	fresh := planSnapshot(map[string]EntryState{
		"primary": {Rel: "primary", Identity: identity, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)})
	plan := PhasePlan{
		ConvergenceID: "drain-budget",
		Kind:          PhaseDrain,
		TargetByRel: map[string]CPUSetTarget{
			"primary": {CPUs: machine.NewCPUSet(1, 2), Mems: "0"},
		},
	}
	budget := NewBudgetTracker(ConvergenceBudget{MaxPlanOperations: 1})
	rebased, err := rebaseDrainPlan(plan, fresh, dag, budget)
	if err != nil {
		t.Fatalf("rebaseDrainPlan error = %v, want phase-aware single shrink", err)
	}
	if got := budget.Usage().Operations; got != 1 {
		t.Fatalf("rebased operations charged = %d, want 1", got)
	}
	if len(rebased.Operations) != 1 ||
		!rebased.Operations[0].Target.CPUs.Equals(machine.NewCPUSet(1)) {
		t.Fatalf("rebased operations = %+v, want one v1-safe shrink to 1", rebased.Operations)
	}

	plan.Operations = []PlanOperation{
		{Rel: "primary/child", Direction: WriteShrink},
		{Rel: "primary", Direction: WriteShrink},
	}
	plan.CostUpperBound.Operations = 1
	if _, err := drainFrontier(plan); !errors.Is(err, ErrPlanOperationBudgetExceeded) {
		t.Fatalf("drainFrontier error = %v, want explicit frontier budget rejection", err)
	}
}

func TestV1DrainRebaseDoesNotWriteEmptyForDisjointDynamicContainerTarget(t *testing.T) {
	t.Parallel()

	const rel = "kubepods/pod/container"
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
		CPUs: machine.NewCPUSet(0, 1), Mems: "0", TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	identity := CgroupIdentity{Device: 1, Inode: 3}
	fresh := planSnapshot(map[string]EntryState{
		"kubepods":     {Rel: "kubepods", Identity: CgroupIdentity{Device: 1, Inode: 1}, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		"kubepods/pod": {Rel: "kubepods/pod", Identity: CgroupIdentity{Device: 1, Inode: 2}, CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		rel:            {Rel: rel, Identity: identity, CPUs: machine.NewCPUSet(0), Mems: "0"},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)})
	fresh.Children = map[string][]ChildRef{
		"kubepods":     {{Name: "pod", Identity: CgroupIdentity{Device: 1, Inode: 2}}},
		"kubepods/pod": {{Name: "container", Identity: identity}},
	}
	fresh.DomainByRel = map[string]DomainID{
		"kubepods": DomainPrimary, "kubepods/pod": DomainPrimary, rel: DomainPrimary,
	}
	fresh.ID = fingerprintSnapshot(fresh)

	plan := PhasePlan{
		ConvergenceID: "v1-disjoint-container-rebase",
		Kind:          PhaseDrain,
		TargetByRel: map[string]CPUSetTarget{
			"kubepods":     {CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
			"kubepods/pod": {CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
			rel:            {CPUs: machine.NewCPUSet(1), Mems: "0"},
		},
	}
	rebased, err := rebaseDrainPlan(plan, fresh, dag, NewBudgetTracker(ConvergenceBudget{}))
	if err != nil {
		t.Fatalf("rebaseDrainPlan: %v", err)
	}
	for _, operation := range rebased.Operations {
		if operation.Target.CPUs.IsEmpty() {
			t.Fatalf("rebased v1 drain emitted empty operation: phase=%s rel=%q current=%s target=%s operations=%+v",
				rebased.Kind, operation.Rel, operation.ExpectedCurrent.CPUs.String(), operation.Target.CPUs.String(), rebased.Operations)
		}
	}
	if got := rebased.TargetByRel[rel].CPUs; !got.Equals(machine.NewCPUSet(0)) {
		t.Fatalf("rebased v1 drain target = %s, want current non-empty hold 0", got.String())
	}
}

func TestV1DrainRebaseHoldsImplicitRuntimeRelThatBecomesNonEmpty(t *testing.T) {
	t.Parallel()

	const rel = "kubepods/pod/container"
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "kubepods", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
		CPUs: machine.NewCPUSet(0, 1), Mems: "0", TrustAnchor: true,
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	fresh := planSnapshot(map[string]EntryState{
		"kubepods":     {Rel: "kubepods", CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		"kubepods/pod": {Rel: "kubepods/pod", CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
		rel:            {Rel: rel, CPUs: machine.NewCPUSet(1), Mems: "0"},
	}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0, 1)})
	fresh.Children = map[string][]ChildRef{
		"kubepods":     {{Name: "pod"}},
		"kubepods/pod": {{Name: "container"}},
	}
	fresh.DomainByRel = map[string]DomainID{
		"kubepods": DomainPrimary, "kubepods/pod": DomainPrimary, rel: DomainPrimary,
	}
	fresh.ID = fingerprintSnapshot(fresh)

	rebased, err := rebaseDrainPlan(PhasePlan{
		ConvergenceID: "v1-empty-target-rebase",
		Kind:          PhaseDrain,
		TargetByRel: map[string]CPUSetTarget{
			"kubepods":     {CPUs: machine.NewCPUSet(0), Mems: "0"},
			"kubepods/pod": {CPUs: machine.NewCPUSet(), Mems: "0"},
			rel:            {CPUs: machine.NewCPUSet(), Mems: "0"},
		},
	}, fresh, dag, NewBudgetTracker(ConvergenceBudget{}))
	if err != nil {
		t.Fatalf("rebaseDrainPlan: %v", err)
	}
	if got := rebased.TargetByRel[rel].CPUs; !got.Equals(machine.NewCPUSet(1)) {
		t.Fatalf("rebased implicit runtime target = %s, want current hold 1", got.String())
	}
	if got := rebased.TargetByRel["kubepods"].CPUs; !got.Equals(machine.NewCPUSet(0, 1)) {
		t.Fatalf("rebased parent target = %s, want held child envelope 0-1", got.String())
	}
	for _, operation := range rebased.Operations {
		if operation.Rel == rel {
			t.Fatalf("rebase emitted runtime-owned rel operation: %+v", operation)
		}
	}
}

func TestPhaseWriterInvalidCPUWriteErrorIncludesPhaseCurrentAndTarget(t *testing.T) {
	t.Parallel()

	driver := newFakeHierarchyDriver()
	identity := CgroupIdentity{Device: 1, Inode: 1}
	driver.add("kubepods/pod/container", identity, "0", "0")
	driver.writeHook = func(_ *fakeHierarchyDriver, _ fakeHierarchyWrite) error {
		return ErrEmptyCPUSetUnsupported
	}
	plan := PhasePlan{
		ConvergenceID: "write-error-context",
		Kind:          PhaseDrain,
		Base: planSnapshot(map[string]EntryState{
			"kubepods/pod/container": {
				Rel: "kubepods/pod/container", Identity: identity,
				CPUs: machine.NewCPUSet(0), Mems: "0",
			},
		}, map[DomainID]machine.CPUSet{DomainPrimary: machine.NewCPUSet(0)}),
		Operations: []PlanOperation{{
			Rel: "kubepods/pod/container", ExpectedIdentity: identity,
			ExpectedCurrent: CPUSetTarget{CPUs: machine.NewCPUSet(0), Mems: "0"},
			Target:          CPUSetTarget{CPUs: machine.NewCPUSet(0, 1), Mems: "0"},
			Direction:       WriteGrow,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID

	err := newSafeCPUSetWriter(driver, NewBudgetTracker(ConvergenceBudget{}), nil).
		execute(context.Background(), plan)
	if err == nil {
		t.Fatal("phase writer unexpectedly succeeded")
	}
	for _, want := range []string{"phase=drain", "current=0", "target=0-1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("phase writer error = %q, want context %q", err.Error(), want)
		}
	}
}

func TestNoWriteBlockedSignatureUsesCgroupGenerationAndMismatchState(t *testing.T) {
	t.Parallel()

	report := ConvergenceReport{NonConvergedTargets: []RelConvergence{{
		Rel: "primary", Observed: machine.NewCPUSet(0), Target: machine.NewCPUSet(0, 1),
		Reason: convergenceReasonTargetMismatch,
	}}}
	snapshotA := &CompleteSnapshot{Entries: map[string]EntryState{
		"primary": {Rel: "primary", Identity: CgroupIdentity{Device: 1, Inode: 11}, CPUs: machine.NewCPUSet(0)},
	}}
	snapshotA.ID[0] = 1
	snapshotB := &CompleteSnapshot{Entries: map[string]EntryState{
		"primary": {Rel: "primary", Identity: CgroupIdentity{Device: 1, Inode: 11}, CPUs: machine.NewCPUSet(0)},
	}}
	snapshotB.ID[0] = 2

	if got, want := noWriteBlockedSignature(snapshotA, nil, report), noWriteBlockedSignature(snapshotB, nil, report); got != want {
		t.Fatalf("per-scan snapshot IDs split identical generation and mismatch:\nfirst=%q\nsecond=%q", got, want)
	}

	snapshotB.Entries["primary"] = EntryState{
		Rel: "primary", Identity: CgroupIdentity{Device: 1, Inode: 12}, CPUs: machine.NewCPUSet(0),
	}
	if got, want := noWriteBlockedSignature(snapshotA, nil, report), noWriteBlockedSignature(snapshotB, nil, report); got == want {
		t.Fatalf("different cgroup generations share no-write signature:\nfirst=%q\nsecond=%q", got, want)
	}
}

func TestTopologyCoordinatorConvergeBlocksAfterRepeatedNoWriteMismatch(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{
		{Rel: "a", Domain: "a", CPUs: machine.NewCPUSet(1), TrustAnchor: true},
		{Rel: "b", Domain: "b", CPUs: machine.NewCPUSet(0), TrustAnchor: true},
	})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["a"] = machine.NewCPUSet(0)
	cg.cpus["b"] = machine.NewCPUSet(1)

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:                  dag,
		Cgroup:               cg,
		CPUDetails:           machine.CPUDetails{0: {}, 1: {}},
		ProtectedCPUSetByRel: map[string]machine.CPUSet{"a": machine.NewCPUSet(0)},
	})
	if err != nil {
		t.Fatalf("Converge: %v", err)
	}
	if res.Converged || res.State != ConvergenceStateBlocked {
		t.Fatalf("result = %+v, want blocked after repeated no-write mismatch", res)
	}
	if got := len(res.Rounds); got != 2 {
		t.Fatalf("rounds = %d, want two identical no-write rounds before blocked; result=%+v", got, res)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("blocked no-write fixed point should not write, writes=%#v", cg.writes)
	}
	if got := len(res.ConvergenceReport.NonConvergedTargets); got == 0 {
		t.Fatalf("blocked result should retain mismatch witness, report=%+v", res.ConvergenceReport)
	}
	if got := res.FirstBlocker(); !strings.Contains(got, `rel="a"`) || !strings.Contains(got, "target_mismatch") {
		t.Fatalf("first blocker = %q, want first convergence mismatch details", got)
	}
}

func TestTopologyCoordinatorConvergeDoesNotRescanAfterStaleRound(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.onApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == "primary" && len(cg.writes) == 0 {
			cg.identities["primary"] = CgroupIdentity{Device: 99, Inode: 99}
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: machine.CPUDetails{0: {}, 1: {}},
	})
	if !errors.Is(err, ErrCgroupIdentityChanged) {
		t.Fatalf("Converge error = %v, want identity-stale cause", err)
	}
	if res.Converged || res.ReplanDisposition != ReplanNotAllowed ||
		res.FinalSnapshotCurrent || res.FinalSnapshot != nil {
		t.Fatalf("result=%+v, want no cross-attempt authority without an adjustment budget", res)
	}
	if got := len(cg.writes); got != 1 {
		t.Fatalf("writes = %d, want one coordinator attempt; writes=%#v", got, cg.writes)
	}
}

func TestTopologyCoordinatorConvergePostWriteRestoreReturnsOwnerReplan(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.afterApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == "primary" {
			cg.cpus[rel] = machine.NewCPUSet(0)
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: machine.CPUDetails{0: {}, 1: {}},
		Budget:     ConvergenceBudget{MaxRounds: 3},
	})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("Converge error = %T %v, want owner-visible stale; result=%+v", err, err, res)
	}
	if res.Converged || res.State != ConvergenceStateNonConverged {
		t.Fatalf("result = %+v, want non-converged stale attempt", res)
	}
	if got := len(res.Rounds); got != 1 {
		t.Fatalf("rounds = %d, want stale not to consume fixed-point rounds; result=%+v", got, res)
	}
	if got := len(res.Journal); got != 1 {
		t.Fatalf("journal = %d, want one partial entry; journal=%+v", got, res.Journal)
	}
	for i, round := range res.Rounds {
		if round.Status != RoundStatusStale || round.Blocker == nil {
			t.Fatalf("round[%d] = %+v, want stale outcome retaining blocker", i, round)
		}
		if got := len(round.Journal); got != 1 {
			t.Fatalf("round[%d] journal = %d, want one partial entry; round=%+v", i, got, round)
		}
		if round.Journal[0].Observed.CPUs.Equals(round.Journal[0].Target.CPUs) {
			t.Fatalf("round[%d] journal = %+v, want restored observation to prove no net progress", i, round.Journal)
		}
	}
	if res.ReplanDisposition != ReplanNotAllowed ||
		res.FinalSnapshotCurrent || res.FinalSnapshot != nil {
		t.Fatalf("result=%+v, want no cross-attempt authority without an adjustment budget", res)
	}
}

func TestLiveFixedPointSessionReturnsJournalPrefixOnStale(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	cg.afterApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == "primary" {
			cg.cpus[rel] = machine.NewCPUSet(0)
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		CPUDetails: machine.CPUDetails{0: {}, 1: {}},
		Budget:     ConvergenceBudget{MaxRounds: 1},
	})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("Converge error = %T %v, want owner-visible stale; result=%+v", err, err, res)
	}
	if len(res.Rounds) != 1 || res.Rounds[0].Status != RoundStatusStale {
		t.Fatalf("rounds=%+v, want one stale single-round engine outcome", res.Rounds)
	}
	if len(res.Journal) != 1 || len(res.Rounds[0].Journal) != 1 {
		t.Fatalf("result journal=%+v round journal=%+v, want the live safe-writer prefix", res.Journal, res.Rounds[0].Journal)
	}
	if !reflect.DeepEqual(res.Journal[0], res.Rounds[0].Journal[0]) {
		t.Fatalf("round journal=%+v, want exact result journal prefix=%+v", res.Rounds[0].Journal, res.Journal)
	}
}

func TestTopologyCoordinatorDrainRefreshReturnsStaleAndPreservesEarlierProgressEvidence(t *testing.T) {
	t.Parallel()

	dag, cg := deepDrainFixture(t)
	rootReadsAfterDeep := 0
	rootReadsBeforeChild := 0
	restoreChildOnce := true
	cg.onApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel == "primary/child" && rootReadsBeforeChild == 0 {
			rootReadsBeforeChild = cg.snapshotRootReads
		}
	}
	cg.afterApply = func(rel string, _ *cgcommon.CPUSetData) {
		switch rel {
		case "primary/child/deep":
			if rootReadsAfterDeep == 0 {
				rootReadsAfterDeep = cg.snapshotRootReads
			}
		case "primary/child":
			if restoreChildOnce {
				restoreChildOnce = false
				cg.cpus[rel] = machine.NewCPUSet(0, 1, 2)
			}
		}
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:                 dag,
		Cgroup:              cg,
		CPUDetails:          machine.CPUDetails{0: {}, 1: {}, 2: {}},
		ExpectedCPUSetByRel: deepDrainDynamicTargets(),
	})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("Converge error = %v, want owner-visible stale", err)
	}
	if res.Converged {
		t.Fatalf("result = %+v, want coordinator attempt to stop at stale frontier", res)
	}
	if rootReadsBeforeChild <= rootReadsAfterDeep {
		t.Fatalf("root snapshot reads deep=%d before-child=%d, want a fresh snapshot between drain frontiers",
			rootReadsAfterDeep, rootReadsBeforeChild)
	}
	if len(res.Rounds) != 1 || res.Rounds[0].Status != RoundStatusStale {
		t.Fatalf("rounds = %+v, want exactly one stale round", res.Rounds)
	}
	stale := res.Rounds[0]
	if stale.Snapshot == nil {
		t.Fatal("stale round lost the fresh snapshot that verified the earlier drain frontier")
	}
	if got := stale.Snapshot.Entries["primary/child/deep"].CPUs; !got.Equals(machine.NewCPUSet(0)) {
		t.Fatalf("stale snapshot deep CPUs = %s, want verified earlier progress 0", got.String())
	}
	if len(stale.ChangedRels) != 1 || stale.ChangedRels[0] != "primary/child/deep" {
		t.Fatalf("stale changed rels = %v, want only verified deep frontier", stale.ChangedRels)
	}
	if res.ReplanDisposition != ReplanNotAllowed ||
		res.FinalSnapshotCurrent || res.FinalSnapshot != nil {
		t.Fatalf("result=%+v, want no cross-attempt authority without an adjustment budget", res)
	}
}

func TestTopologyCoordinatorExternalRestoreClearsEarlierDrainBatchProgress(t *testing.T) {
	t.Parallel()

	dag, cg := deepDrainFixture(t)
	cg.afterApply = func(rel string, _ *cgcommon.CPUSetData) {
		if rel != "primary/child" {
			return
		}
		cg.cpus["primary/child"] = machine.NewCPUSet(0, 1, 2)
		cg.cpus["primary/child/deep"] = machine.NewCPUSet(0, 1, 2)
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:                 dag,
		Cgroup:              cg,
		CPUDetails:          machine.CPUDetails{0: {}, 1: {}, 2: {}},
		ExpectedCPUSetByRel: deepDrainDynamicTargets(),
		Budget:              ConvergenceBudget{MaxRounds: 4},
	})
	if !errors.Is(err, ErrCoordinatorPlanStale) {
		t.Fatalf("Converge error = %T %v, want owner-visible stale after external restore; result=%+v", err, err, res)
	}
	if got := len(res.Rounds); got != 1 {
		t.Fatalf("rounds = %d, want no internal stale recompiles; result=%+v", got, res)
	}
	for i, round := range res.Rounds {
		if round.Snapshot == nil {
			t.Fatalf("round[%d] lost fresh recovery snapshot: %+v", i, round)
		}
		if len(round.ChangedRels) != 0 {
			t.Fatalf("round[%d] changed rels = %v, want external restore to clear net progress", i, round.ChangedRels)
		}
	}
}

func deepDrainFixture(t *testing.T) (*TopoDAG, *topologyFakeCgroup) {
	t.Helper()
	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1, 2)
	cg.cpus["primary/child"] = machine.NewCPUSet(0, 1, 2)
	cg.cpus["primary/child/deep"] = machine.NewCPUSet(0, 1, 2)
	cg.children["primary"] = []string{"child"}
	cg.children["primary/child"] = []string{"deep"}
	return dag, cg
}

func deepDrainDynamicTargets() map[string]machine.CPUSet {
	return map[string]machine.CPUSet{
		"primary/child":      machine.NewCPUSet(0),
		"primary/child/deep": machine.NewCPUSet(0),
	}
}

func TestTopologyCoordinatorExplicitIOAutoPlanBudgetConverges300DeepDynamicDescendants(t *testing.T) {
	t.Parallel()
	const dynamicDepth = 300

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0, 1)
	cg.files["primary"] = map[string][]byte{"cpuset.mems": []byte("0")}
	dynamicTargets := make(map[string]machine.CPUSet, dynamicDepth)
	parent := "primary"
	for i := 0; i < dynamicDepth; i++ {
		name := fmt.Sprintf("child-%03d", i)
		rel := parent + "/" + name
		cg.children[parent] = []string{name}
		cg.cpus[rel] = machine.NewCPUSet(0, 1)
		cg.files[rel] = map[string][]byte{"cpuset.mems": []byte("0")}
		dynamicTargets[rel] = machine.NewCPUSet(0)
		parent = rel
	}

	res, err := (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:                 dag,
		Cgroup:              cg,
		CPUDetails:          machine.CPUDetails{0: {}, 1: {}},
		ExpectedCPUSetByRel: dynamicTargets,
		Budget: ConvergenceBudget{
			MaxHierarchyIOOperations: 1_000_000,
			MaxSnapshotDepth:         512,
			// This test validates operation-budget sizing and convergence, not
			// elapsed time. Keep structural limits active without a wall-clock cap.
			DeadlineDuration: -1,
		},
	})
	if err != nil {
		if !errors.Is(err, ErrPlanOperationBudgetExceeded) {
			t.Fatalf("Converge error = %v, want successful convergence (old code must fail with typed plan budget error)", err)
		}
		t.Fatalf("Converge exhausted auto plan budget: %v; rounds=%d writes=%d", err, len(res.Rounds), len(cg.writes))
	}
	if !res.Converged || !res.FinalSnapshotCurrent {
		t.Fatalf("Converge result = %+v, want current converged snapshot", res)
	}
	if got := res.FinalSnapshot.Entries[parent].CPUs; !got.Equals(machine.NewCPUSet(0)) {
		t.Fatalf("deepest descendant CPUs = %s, want 0", got.String())
	}
}

func TestCoordinatorReclassifiesDormantBucketAtPlanningBoundary(t *testing.T) {
	t.Parallel()

	dag := mustPlanDAG(t, []NodeSpec{
		{
			Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
			CPUs: machine.NewCPUSet(0, 1), TrustAnchor: true,
		},
		{
			Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim,
			CPUs: machine.NewCPUSet(), TrustAnchor: true,
		},
		{
			Rel: "reclaim/bucket-0", ParentRel: "reclaim",
			Role: TopoNodeRoleReclaimNUMABucket, Domain: DomainReclaim,
			CPUs: machine.NewCPUSet(),
			Constraint: TopologyConstraint{
				CPUUpperBound: machine.NewCPUSet(0, 1),
				Scope:         TopologyScopeNUMANode,
			},
		},
	})
	inactive := CgroupActivity{TasksEmpty: true, CgroupProcsEmpty: true, Childless: true}
	first := planSnapshot(map[string]EntryState{
		"primary":          {Identity: CgroupIdentity{Inode: 1}, CPUs: machine.NewCPUSet(0, 1)},
		"reclaim":          {Identity: CgroupIdentity{Inode: 2}, CPUs: machine.NewCPUSet(0, 1)},
		"reclaim/bucket-0": {Identity: CgroupIdentity{Inode: 3}, CPUs: machine.NewCPUSet(0, 1), Activity: inactive},
	}, nil)
	first.Children = map[string][]ChildRef{
		"primary": {}, "reclaim": {{Name: "bucket-0", Identity: CgroupIdentity{Inode: 3}}},
		"reclaim/bucket-0": {},
	}
	round := &coordinatorRound{
		dag:                 dag,
		semanticTargetByRel: desiredTargets(dag),
		allowEmptyTarget:    false,
		pendingSnapshot:     first,
	}

	snapshot, err := round.nextPlanningSnapshot(context.Background())
	if err != nil {
		t.Fatalf("first nextPlanningSnapshot() error = %v", err)
	}
	if _, dormant := round.dormantRels["reclaim/bucket-0"]; !dormant {
		t.Fatalf("dormant rels = %v, want empty inactive bucket", round.dormantRels)
	}
	if got, _ := snapshot.TargetProofCPUs("reclaim/bucket-0", machine.NewCPUSet()); !got.IsEmpty() {
		t.Fatalf("first semantic ownership = %s, want empty", got.String())
	}

	second := *first
	second.Entries = make(map[string]EntryState, len(first.Entries))
	for rel, observed := range first.Entries {
		second.Entries[rel] = observed
	}
	entry := second.Entries["reclaim/bucket-0"]
	entry.Activity.TasksEmpty = false
	second.Entries["reclaim/bucket-0"] = entry
	round.pendingSnapshot = &second

	snapshot, err = round.nextPlanningSnapshot(context.Background())
	if err != nil {
		t.Fatalf("second nextPlanningSnapshot() error = %v", err)
	}
	if _, dormant := round.dormantRels["reclaim/bucket-0"]; dormant {
		t.Fatalf("dormant rels = %v, want active bucket after task arrival", round.dormantRels)
	}
	if len(snapshot.OwnershipByRel) != 0 {
		t.Fatalf("ownership overlay = %v, want raw active snapshot", snapshot.OwnershipByRel)
	}
}

func TestModeGuardRejectsDifferentConcurrentModeWithTypedBusy(t *testing.T) {
	t.Parallel()

	gate := NewModeGate()
	token, err := NormalModeGuardWithGate(gate).TryEnter()
	if err != nil {
		t.Fatalf("TryEnter normal: %v", err)
	}
	defer token.Exit()

	_, err = ResetModeGuardWithGate(gate).TryEnter()
	var busy *CoordinatorBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("TryEnter reset error = %T %v, want *CoordinatorBusyError", err, err)
	}
	if busy.Requested != CoordinatorModeReset || busy.Active != CoordinatorModeNormal {
		t.Fatalf("busy = %+v, want requested reset active normal", busy)
	}
}

func TestTopologyCoordinatorBusyReturnsBeforeWrites(t *testing.T) {
	t.Parallel()

	dag, err := BuildDAG([]NodeSpec{{
		Rel: "primary", Role: TopoNodeRolePrimary, CPUs: machine.NewCPUSet(0, 1), Mems: "0",
	}})
	if err != nil {
		t.Fatalf("BuildDAG: %v", err)
	}
	cg := newTopologyFakeCgroup()
	cg.cpus["primary"] = machine.NewCPUSet(0)
	gate := NewModeGate()
	token, err := ResetModeGuardWithGate(gate).TryEnter()
	if err != nil {
		t.Fatalf("TryEnter reset: %v", err)
	}
	defer token.Exit()

	_, err = (TopologyCoordinator{}).Converge(context.Background(), CoordinatorInput{
		DAG:        dag,
		Cgroup:     cg,
		Mode:       NormalModeGuardWithGate(gate),
		CPUDetails: machine.CPUDetails{0: {}, 1: {}},
	})
	var busy *CoordinatorBusyError
	if !errors.As(err, &busy) {
		t.Fatalf("Converge error = %T %v, want *CoordinatorBusyError", err, err)
	}
	if len(cg.writes) != 0 {
		t.Fatalf("busy coordinator must not write, writes=%#v", cg.writes)
	}
}
