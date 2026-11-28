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
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

func TestProjectedPhaseSessionSnapshotHonorsCanceledContext(t *testing.T) {
	t.Parallel()
	_, base := newTask9ParentSafeFixture(t)
	session, err := newProjectedPhaseSession(base, base.Capabilities)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = session.Snapshot(ctx)

	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, ErrConvergenceDeadlineExceeded)
}

func TestProjectedPhaseSessionSnapshotDoesNotPublishCloneWhenCanceledAfterClone(t *testing.T) {
	t.Parallel()
	_, base := newTask9ParentSafeFixture(t)
	session, err := newProjectedPhaseSession(base, base.Capabilities)
	require.NoError(t, err)
	ctx := &traceValidationCancellationContext{
		Context:  context.Background(),
		cancelOn: 2,
	}

	snapshot, err := session.Snapshot(ctx)

	require.Nil(t, snapshot)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, ErrConvergenceDeadlineExceeded)
	require.Equal(t, 2, ctx.checks)
}

func TestProjectedPhaseSessionApplyHonorsCancellationBeforeOperation(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	session := &projectedPhaseSession{
		hierarchy: hierarchy,
		progress:  make(map[phaseProgressKey]struct{}),
	}
	operation := hierarchy.cpuOperation(
		projectedChildRel,
		machine.NewCPUSet(),
		machine.NewCPUSet(0),
	)
	plan := PhasePlan{Kind: PhaseExpand, Operations: []PlanOperation{operation}}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	before := CloneCompleteSnapshot(session.hierarchy.snapshot)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := session.Apply(ctx, plan)

	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, ErrConvergenceDeadlineExceeded)
	require.Zero(t, result.Applied)
	require.Empty(t, result.Journal)
	require.Equal(t, before, session.hierarchy.snapshot)
}

func TestProjectedPhaseSessionApplyPreservesPartialResultWithoutPublishingCandidateOnCancellation(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	for _, rel := range []string{projectedChildRel, projectedInheritRel} {
		entry := hierarchy.snapshot.Entries[rel]
		entry.CPUs = machine.NewCPUSet(0)
		entry.ConfiguredCPUs = machine.NewCPUSet(0)
		hierarchy.snapshot.Entries[rel] = entry
	}
	require.NoError(t, hierarchy.recomputeEvidence())
	session := &projectedPhaseSession{
		hierarchy: hierarchy,
		progress:  make(map[phaseProgressKey]struct{}),
	}
	plan := PhasePlan{
		Kind: PhaseExpand,
		Operations: []PlanOperation{
			hierarchy.cpuOperation(projectedChildRel, machine.NewCPUSet(0), machine.MustParse("0-1")),
			hierarchy.cpuOperation(projectedInheritRel, machine.NewCPUSet(0), machine.MustParse("0-1")),
		},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}
	before := CloneCompleteSnapshot(session.hierarchy.snapshot)
	ctx := &traceValidationCancellationContext{
		Context:  context.Background(),
		cancelOn: 4,
	}

	result, err := session.Apply(ctx, plan)

	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, ErrConvergenceDeadlineExceeded)
	require.Equal(t, 1, result.Applied)
	require.Len(t, result.Journal, 1)
	require.Equal(t, plan.Operations[0].Rel, result.Journal[0].Rel)
	require.Equal(t, before, session.hierarchy.snapshot,
		"partially applied candidate must not replace the published projected hierarchy")
}

func TestProjectedPhaseSessionApplyCanceledPlanCanRetryWithFreshContext(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	for _, rel := range []string{projectedChildRel, projectedInheritRel} {
		entry := hierarchy.snapshot.Entries[rel]
		entry.CPUs = machine.NewCPUSet(0)
		entry.ConfiguredCPUs = machine.NewCPUSet(0)
		hierarchy.snapshot.Entries[rel] = entry
	}
	require.NoError(t, hierarchy.recomputeEvidence())
	session := &projectedPhaseSession{
		hierarchy: hierarchy,
		progress:  make(map[phaseProgressKey]struct{}),
	}
	plan := PhasePlan{
		Kind: PhaseExpand,
		Operations: []PlanOperation{
			hierarchy.cpuOperation(projectedChildRel, machine.NewCPUSet(0), machine.MustParse("0-1")),
			hierarchy.cpuOperation(projectedInheritRel, machine.NewCPUSet(0), machine.MustParse("0-1")),
		},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	for i := range plan.Operations {
		plan.Operations[i].PlanID = plan.PlanID
	}
	key := phaseProgressKey{SnapshotID: hierarchy.snapshot.ID, PlanID: plan.PlanID}
	canceled := &traceValidationCancellationContext{
		Context:  context.Background(),
		cancelOn: 4,
	}

	_, err := session.Apply(canceled, plan)
	require.ErrorIs(t, err, context.Canceled)
	require.NotContains(t, session.progress, key,
		"an uncommitted candidate must not consume the plan progress key")

	result, err := session.Apply(context.Background(), plan)
	require.NoError(t, err)
	require.Equal(t, len(plan.Operations), result.Applied)
	require.Contains(t, session.progress, key)
	require.True(t, session.hierarchy.snapshot.Entries[projectedChildRel].CPUs.Equals(machine.MustParse("0-1")))
	require.True(t, session.hierarchy.snapshot.Entries[projectedInheritRel].CPUs.Equals(machine.MustParse("0-1")))
}

func TestCompileValidatedFixedPointTraceCanceledEntryCarriesCoordinatorUsage(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	fixture.round.round = 3
	require.NoError(t, fixture.budget.ConsumePlanOperations(7))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	trace, err := fixture.round.compileValidatedFixedPointTrace(ctx, fixture.snapshot())

	require.Nil(t, trace)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, ErrConvergenceDeadlineExceeded)
	var deadlineErr *convergenceDeadlineContextError
	require.ErrorAs(t, err, &deadlineErr)
	require.True(t, deadlineErr.hasCoordinatorState)
	require.Equal(t, 3, deadlineErr.rounds)
	require.Equal(t, fixture.budget.Usage(), deadlineErr.usage)
}

func TestProjectedPhaseSessionCanceledEntryDoesNotInventCoordinatorUsage(t *testing.T) {
	t.Parallel()
	_, base := newTask9ParentSafeFixture(t)
	session, err := newProjectedPhaseSession(base, base.Capabilities)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = session.Snapshot(ctx)

	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, ErrConvergenceDeadlineExceeded)
	var deadlineErr *convergenceDeadlineContextError
	require.ErrorAs(t, err, &deadlineErr)
	require.False(t, deadlineErr.hasCoordinatorState)
	require.NotContains(t, err.Error(), "rounds=")
	require.NotContains(t, err.Error(), "usage=")
}

func TestFrozenTraceValidationHonorsCancellationDuringOperationReplay(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	require.Greater(t, trace.OperationCount(), 1)
	ctx := &traceValidationCancellationContext{
		Context:  context.Background(),
		cancelOn: 4,
	}

	err := validateTraceOperations(ctx, trace)

	require.ErrorIs(t, err, context.Canceled)
	require.GreaterOrEqual(t, ctx.checks, ctx.cancelOn)
}

func TestFrozenGrowReleaseGuardCompilationHonorsCancellation(t *testing.T) {
	t.Parallel()
	trace, _ := compiledTraceWithCPUAndMemoryWrites(t)
	require.Greater(t, trace.OperationCount(), 1)
	ctx := &traceValidationCancellationContext{
		Context:  context.Background(),
		cancelOn: 4,
	}

	_, err := compileFrozenGrowReleaseGuards(ctx, trace)

	require.ErrorIs(t, err, context.Canceled)
	require.GreaterOrEqual(t, ctx.checks, ctx.cancelOn)
}

func TestCompileValidatedFixedPointTracePropagatesContextThroughValidation(t *testing.T) {
	t.Parallel()
	for _, target := range []string{
		"validateFrozenPhaseTrace",
		"compileFrozenGrowReleaseGuards",
	} {
		target := target
		t.Run(target, func(t *testing.T) {
			t.Parallel()
			fixture := newAdmissionTraceFixture(t)
			fixture.configureStagedSMTTransferWithDynamicDescendant()
			ctx := &cancelInFunctionContext{
				Context: context.Background(),
				target:  target,
			}

			trace, err := fixture.round.compileValidatedFixedPointTrace(
				ctx, fixture.snapshot())

			require.Nil(t, trace)
			require.ErrorIs(t, err, context.Canceled)
			require.True(t, ctx.seen, "caller context must reach %s", target)
		})
	}
}

type traceValidationCancellationContext struct {
	context.Context
	checks   int
	cancelOn int
}

func (c *traceValidationCancellationContext) Err() error {
	c.checks++
	if c.checks >= c.cancelOn {
		return context.Canceled
	}
	return c.Context.Err()
}

type cancelInFunctionContext struct {
	context.Context
	target string
	seen   bool
}

func (c *cancelInFunctionContext) Err() error {
	callers := make([]uintptr, 16)
	count := runtime.Callers(2, callers)
	for _, caller := range callers[:count] {
		function := runtime.FuncForPC(caller)
		if function != nil && strings.HasSuffix(function.Name(), "."+c.target) {
			c.seen = true
			return context.Canceled
		}
	}
	return c.Context.Err()
}

const topologyScaleTestEnv = "KATALYST_TOPOLOGY_SCALE_TEST"

// admissionTraceFixture wires a coordinatorRound to a fake hierarchy driver so
// tests can compile a complete Drain->Expand fixed point on a cloned snapshot
// without touching any live driver. The live fake driver is only inspected to
// prove that compilation performs zero physical writes.
type admissionTraceFixture struct {
	t *testing.T

	// driver is the live hierarchy driver. Compilation must never write to it.
	driver *fakeHierarchyDriver
	// projectedDriver records writes attempted against the projected clone and
	// can be told to drop them to simulate a non-converging fixed point.
	projectedDriver *recordingProjectedDriver

	round     *coordinatorRound
	selection DrainSelectionPolicy

	specs          []NodeSpec
	targetByRel    map[string]machine.CPUSet
	requiredByRel  map[string]machine.CPUSet
	dynamicByRel   map[string]machine.CPUSet
	canonicalByRel map[string]machine.CPUSet

	cpuDetails machine.CPUDetails
	budget     *BudgetTracker

	nextInode uint64
}

// recordingProjectedDriver is a test double for the clone-backed projection the
// compiler uses. IgnoreWrites drops every projected mutation so the fixed point
// can never make progress.
type recordingProjectedDriver struct {
	IgnoreWrites bool
}

// liveTraceSession adapts the live fake driver to the phase execution session
// contract so the engine can be replayed against real snapshots for parity.
type liveTraceSession struct {
	round *coordinatorRound
	base  *CompleteSnapshot
}

func newLiveTraceSession(round *coordinatorRound, base *CompleteSnapshot) *liveTraceSession {
	return &liveTraceSession{round: round, base: base}
}

func (s *liveTraceSession) Snapshot(ctx context.Context) (*CompleteSnapshot, error) {
	if s.round.snapshotSource == nil {
		return s.base, nil
	}
	return s.round.snapshotSource(ctx)
}

func (s *liveTraceSession) Apply(ctx context.Context, plan PhasePlan) (phaseSessionApplyResult, error) {
	result := phaseSessionApplyResult{}
	for _, op := range plan.Operations {
		if op.WriteMems {
			if err := s.round.driver.WriteMems(ctx, op.Rel, op.ExpectedIdentity, op.Target.Mems); err != nil {
				return result, err
			}
		}
		if !op.ExpectedCurrent.CPUs.Equals(op.Target.CPUs) {
			if err := s.round.driver.WriteCPUs(ctx, op.Rel, op.ExpectedIdentity, op.Target.CPUs); err != nil {
				return result, err
			}
		}
		result.Applied++
		result.Journal = append(result.Journal, AppliedPlanOperation{
			PlanID: op.PlanID, Rel: op.Rel, Direction: op.Direction,
			Target: op.Target, Observed: op.Target,
		})
	}
	return result, nil
}

func (s *liveTraceSession) Capabilities() HierarchyCapabilities {
	return s.round.driver.Capabilities()
}

// projectedTraceSession adapts a clone-backed projectedHierarchy to the phase
// execution session contract. Snapshot returns an isolated clone so the engine
// can freeze plan.Base before Apply mutates the projection in place.
type projectedTraceSession struct {
	hierarchy    *projectedHierarchy
	ignoreWrites bool
}

// snapshotMetadataChurnSession changes fingerprinted but plan-irrelevant
// configured mems evidence on every snapshot. It models concurrent hierarchy
// churn that changes SnapshotID without advancing the protected CPU transfer.
type snapshotMetadataChurnSession struct {
	phaseExecutionSession
	rel  string
	next int
}

func (s *snapshotMetadataChurnSession) Snapshot(ctx context.Context) (*CompleteSnapshot, error) {
	snapshot, err := s.phaseExecutionSession.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	entry := snapshot.Entries[s.rel]
	entry.ConfiguredMems = fmt.Sprintf("snapshot-churn-%d", s.next)
	s.next++
	snapshot.Entries[s.rel] = entry
	snapshot.ID = fingerprintSnapshot(snapshot)
	return snapshot, nil
}

func newProjectedTraceSession(t *testing.T, base *CompleteSnapshot, capabilities HierarchyCapabilities) *projectedTraceSession {
	t.Helper()
	hierarchy, err := newProjectedHierarchy(base, capabilities)
	require.NoError(t, err)
	return &projectedTraceSession{hierarchy: hierarchy}
}

func (s *projectedTraceSession) Snapshot(_ context.Context) (*CompleteSnapshot, error) {
	return CloneCompleteSnapshot(s.hierarchy.snapshot), nil
}

func (s *projectedTraceSession) Apply(_ context.Context, plan PhasePlan) (phaseSessionApplyResult, error) {
	if s.ignoreWrites {
		return phaseSessionApplyResult{}, nil
	}
	result := phaseSessionApplyResult{}
	for _, operation := range plan.Operations {
		if err := s.hierarchy.applyOperation(operation); err != nil {
			return result, err
		}
		result.Applied++
		result.Journal = append(result.Journal, AppliedPlanOperation{
			PlanID: operation.PlanID, Rel: operation.Rel, Direction: operation.Direction,
			Target: operation.Target, Observed: operation.Target,
		})
	}
	return result, nil
}

func (s *projectedTraceSession) Capabilities() HierarchyCapabilities {
	return s.hierarchy.capabilities
}

func newAdmissionTraceFixture(t *testing.T) *admissionTraceFixture {
	t.Helper()
	f := &admissionTraceFixture{
		t:               t,
		driver:          newFakeHierarchyDriver(),
		projectedDriver: &recordingProjectedDriver{},
		selection:       DefaultDrainSelectionPolicy(),
		targetByRel:     map[string]machine.CPUSet{},
		requiredByRel:   map[string]machine.CPUSet{},
		dynamicByRel:    map[string]machine.CPUSet{},
		canonicalByRel:  map[string]machine.CPUSet{},
		cpuDetails: machine.CPUDetails{
			0: {NUMANodeID: 0},
			1: {NUMANodeID: 0},
			2: {NUMANodeID: 0},
			3: {NUMANodeID: 0},
		},
		budget:    NewBudgetTracker(ConvergenceBudget{}),
		nextInode: 1,
	}
	// Unwitnessed expansion is authorized by the engine's own proof, not by the
	// invariant driver used only for negative low-level tests here.
	f.driver.allowUnwitnessedExpansion = true
	f.round = &coordinatorRound{
		objective:    ConvergenceObjectiveParentSafe,
		budget:       f.budget,
		cpuDetails:   f.cpuDetails,
		reservedCPUs: machine.NewCPUSet(),
		blocked:      map[DomainID]machine.CPUSet{},
		maxRounds:    64,
	}
	return f
}

func (f *admissionTraceFixture) allocInode() uint64 {
	inode := f.nextInode
	f.nextInode++
	return inode
}

func (f *admissionTraceFixture) parentOf(rel string) string {
	if idx := strings.LastIndex(rel, "/"); idx >= 0 {
		return rel[:idx]
	}
	return ""
}

func (f *admissionTraceFixture) addPrimary(rel, cpus, mems string) {
	f.driver.add(rel, CgroupIdentity{Device: 1, Inode: f.allocInode()}, cpus, mems)
	f.specs = append(f.specs, NodeSpec{
		Rel: rel, Role: TopoNodeRolePrimary, Domain: DomainPrimary,
		CPUs: machine.MustParse(cpus), Mems: mems,
		ParentRel: f.parentOf(rel), TrustAnchor: f.parentOf(rel) == "",
	})
	f.targetByRel[rel] = machine.MustParse(cpus)
}

func (f *admissionTraceFixture) addDynamicDescendant(rel, cpus, mems string) {
	f.driver.add(rel, CgroupIdentity{Device: 1, Inode: f.allocInode()}, cpus, mems)
	f.specs = append(f.specs, NodeSpec{
		Rel: rel, Role: TopoNodeRolePrimary, Domain: DomainPrimary,
		CPUs: machine.MustParse(cpus), Mems: mems,
		ParentRel: f.parentOf(rel),
	})
	f.targetByRel[rel] = machine.MustParse(cpus)
	f.dynamicByRel[rel] = machine.MustParse(cpus)
}

func (f *admissionTraceFixture) addReclaim(rel, cpus, mems string) {
	f.driver.add(rel, CgroupIdentity{Device: 1, Inode: f.allocInode()}, cpus, mems)
	f.specs = append(f.specs, NodeSpec{
		Rel: rel, Role: TopoNodeRoleReclaim, Domain: DomainReclaim,
		CPUs: machine.MustParse(cpus), Mems: mems,
		ParentRel: f.parentOf(rel), TrustAnchor: f.parentOf(rel) == "",
	})
	f.targetByRel[rel] = machine.MustParse(cpus)
}

func (f *admissionTraceFixture) requireCPUSet(rel, cpus string) {
	set := machine.MustParse(cpus)
	f.requiredByRel[rel] = set
	f.targetByRel[rel] = set
}

func (f *admissionTraceFixture) setCanonicalTarget(rel, cpus string) {
	f.canonicalByRel[rel] = machine.MustParse(cpus)
}

func (f *admissionTraceFixture) configureStagedSMTTransferWithDynamicDescendant() {
	f.selection.MaxCPUsDrainRatio = 0.5
	f.driver.capabilities = cgroupV2Policy.capabilities(true)
	f.round.allowEmptyTarget = true
	f.cpuDetails[4] = machine.CPUTopoInfo{NUMANodeID: 0}
	f.addPrimary("kubepods", "1-3", "0")
	f.addDynamicDescendant("kubepods/besteffort", "1-3", "0")
	f.addReclaim("reclaimed", "4", "0")
	f.addReclaim("reclaimed/leaf", "0", "0")
	f.specs[len(f.specs)-1].CPUs = machine.NewCPUSet()
	f.targetByRel["reclaimed/leaf"] = machine.NewCPUSet()
	f.requireCPUSet("kubepods", "0-3")
	f.requireCPUSet("kubepods/besteffort", "0-3")
}

func (f *admissionTraceFixture) configureMultiFrontierParentSafeDrain() {
	f.driver.capabilities = cgroupV2Policy.capabilities(true)
	f.addPrimary("kubepods", "0-3", "0")
	f.addDynamicDescendant("kubepods/besteffort", "0-3", "0")
	f.addReclaim("reclaimed", "4", "0")
	f.targetByRel["kubepods"] = machine.MustParse("0-2")
	f.targetByRel["kubepods/besteffort"] = machine.MustParse("0-2")
	f.dynamicByRel["kubepods/besteffort"] = machine.MustParse("0-2")
	f.requireCPUSet("reclaimed", "3-4")
}

func (f *admissionTraceFixture) addUnavailableDynamicChild(rel, cpus, mems string) {
	f.driver.add(rel, CgroupIdentity{Device: 1, Inode: f.allocInode()}, cpus, mems)
	f.driver.beforeCall = func(op HierarchyOperation, candidate string) error {
		if op == HierarchyOperationRead && candidate == rel {
			return ErrCgroupControllerUnavailable
		}
		return nil
	}
}

func (f *admissionTraceFixture) configureProtectedDeferredEvaluationInputs() {
	f.addDynamicDescendant("kubepods/deferred", "1-3", "0")
	f.targetByRel["kubepods/deferred"] = machine.MustParse("0-3")
	f.round.protectedPending = machine.NewCPUSet(1)
	f.round.deferredByRel = map[string]machine.CPUSet{
		"kubepods/deferred": machine.MustParse("1-3"),
	}
}

func (f *admissionTraceFixture) configureAllProtectedSwap() {
	f.driver.add("primary", CgroupIdentity{Device: 1, Inode: f.allocInode()}, "0", "0")
	f.driver.add("reclaim", CgroupIdentity{Device: 1, Inode: f.allocInode()}, "1", "0")
	f.specs = append(f.specs,
		NodeSpec{
			Rel: "primary", Role: TopoNodeRolePrimary, Domain: DomainPrimary,
			CPUs: machine.NewCPUSet(1), Mems: "0", TrustAnchor: true,
		},
		NodeSpec{
			Rel: "reclaim", Role: TopoNodeRoleReclaim, Domain: DomainReclaim,
			CPUs: machine.NewCPUSet(0), Mems: "0", TrustAnchor: true,
		},
	)
	f.targetByRel["primary"] = machine.NewCPUSet(1)
	f.targetByRel["reclaim"] = machine.NewCPUSet(0)
	f.round.protectedByRel = map[string]machine.CPUSet{
		"primary": machine.NewCPUSet(0),
		"reclaim": machine.NewCPUSet(1),
	}
}

// snapshot wires the round to the current fixture topology and returns a fresh
// complete snapshot captured from the live fake driver.
func (f *admissionTraceFixture) snapshot() *CompleteSnapshot {
	f.t.Helper()
	dag, err := BuildDAG(f.specs)
	require.NoError(f.t, err)

	f.round.dag = dag
	f.round.driver = f.driver
	f.round.selection = NormalizeDrainSelectionPolicy(f.selection)
	f.round.targetByRel = model.CloneCPUSetMap(f.targetByRel)
	f.round.requiredByRel = model.CloneCPUSetMap(f.requiredByRel)
	f.round.dynamicByRel = model.CloneCPUSetMap(f.dynamicByRel)
	f.round.snapshotSource = newCompleteSnapshotSource(f.driver, dag, f.budget)

	base, err := f.round.snapshotSource(context.Background())
	require.NoError(f.t, err)
	return base
}

func (f *admissionTraceFixture) runFixedPointAgainstRecordingDriver(t *testing.T) []PlanOperation {
	t.Helper()
	base := f.snapshot()
	result, err := f.round.runFixedPointEngine(
		context.Background(),
		newLiveTraceSession(f.round, base),
	)
	require.NoError(t, err)
	return flattenCompiledPhases(result.Phases)
}

// flattenCompiledPhases collapses ordered phases into a single operation slice
// so compiled and live traces can be compared operation-by-operation.
func flattenCompiledPhases(phases []CompiledPhase) []PlanOperation {
	operations := make([]PlanOperation, 0, len(phases))
	for _, phase := range phases {
		operations = append(operations, phase.Operations...)
	}
	return operations
}

func flattenTraceOperations(trace *CompiledPhaseTrace) []PlanOperation {
	if trace == nil {
		return nil
	}
	return flattenCompiledPhases(trace.Phases)
}

func traceContainsOperation(
	trace *CompiledPhaseTrace,
	rel string,
	direction WriteDirection,
	target machine.CPUSet,
) bool {
	if trace == nil {
		return false
	}
	for _, op := range flattenTraceOperations(trace) {
		if op.Rel == rel && op.Direction == direction && op.Target.CPUs.Equals(target) {
			return true
		}
	}
	return false
}

func TestCompileFixedPointTraceIncludesStagedDynamicDescendantGrow(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()

	trace, err := fixture.round.compileFixedPointTrace(
		context.Background(),
		fixture.snapshot(),
	)
	require.NoError(t, err)

	require.Greater(t, len(trace.Phases), 1)
	require.True(t, traceContainsOperation(
		trace,
		"kubepods/besteffort",
		WriteGrow,
		machine.MustParse("0-3"),
	))
	require.True(t, trace.FinalEvaluation.ParentSafety.Safe)
	require.NotEmpty(t, trace.TraceID)
	require.NotEmpty(t, trace.ConvergenceID)
	require.Equal(t, ConvergenceObjectiveParentSafe, trace.Objective)
	require.NotNil(t, trace.InitialSnapshot)
	require.NotNil(t, trace.FinalSnapshot)
	require.Equal(t, fixture.driver.Capabilities(), trace.Capabilities)
	require.Equal(t, machine.MustParse("0-3"), trace.RequiredCPUSetByRel["kubepods"])
	require.Equal(t, machine.MustParse("0-3"), trace.CanonicalTargetByRel["kubepods"].CPUs)
	require.Positive(t, trace.Cost.Forward.Total())
	require.Equal(t, trace.Cost.Forward, trace.Cost.Rollback)
	require.Zero(t, fixture.driver.PhysicalWriteCount())
}

func TestCompileFixedPointTraceStagesPendingSourceDrainAndDefersUnrelatedSiblingShrink(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.driver.capabilities = cgroupV2Policy.capabilities(true)
	fixture.round.allowEmptyTarget = true
	fixture.selection.MaxCPUsDrainRatio = 0.25
	for cpu := 4; cpu < 192; cpu++ {
		fixture.cpuDetails[cpu] = machine.CPUTopoInfo{NUMANodeID: 0}
	}

	const (
		allCPUs       = "0-191"
		pendingCPUs   = "0-103"
		scopeCPUs     = "0-51"
		remainingCPUs = "104-191"
	)
	fixture.addPrimary("primary", pendingCPUs, "0")
	fixture.addDynamicDescendant("primary/scope", scopeCPUs, "0")
	fixture.addDynamicDescendant("primary/sibling", pendingCPUs, "0")
	fixture.addReclaim("reclaim", allCPUs, "0")
	fixture.addReclaim("reclaim/bucket", allCPUs, "0")
	fixture.addReclaim("reclaim/bucket/leaf", allCPUs, "0")

	fixture.targetByRel["primary/scope"] = machine.MustParse(pendingCPUs)
	fixture.dynamicByRel["primary/scope"] = machine.MustParse(pendingCPUs)
	fixture.targetByRel["primary/sibling"] = machine.NewCPUSet()
	fixture.dynamicByRel["primary/sibling"] = machine.NewCPUSet()
	fixture.targetByRel["reclaim"] = machine.MustParse(remainingCPUs)
	fixture.targetByRel["reclaim/bucket"] = machine.MustParse(remainingCPUs)
	fixture.targetByRel["reclaim/bucket/leaf"] = machine.MustParse(remainingCPUs)
	for i := range fixture.specs {
		fixture.specs[i].CPUs = fixture.targetByRel[fixture.specs[i].Rel].Clone()
	}
	fixture.round.protectedPending = machine.MustParse(pendingCPUs)
	fixture.round.pendingRequiredByRel = map[string]machine.CPUSet{
		"primary":       machine.MustParse(pendingCPUs),
		"primary/scope": machine.MustParse(pendingCPUs),
	}
	fixture.round.deferredByRel = map[string]machine.CPUSet{
		"primary/sibling": machine.NewCPUSet(),
	}

	base := fixture.snapshot()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err)
	require.True(t, trace.FinalEvaluation.ParentSafety.Safe)
	require.Zero(t, fixture.driver.PhysicalWriteCount())

	sourceDrainPhases := 0
	sawPartialSourceDrain := false
	for _, phase := range trace.Phases {
		if phase.Kind != PhaseDrain {
			continue
		}
		for _, operation := range phase.Operations {
			require.NotEqual(t, "primary/sibling", operation.Rel,
				"unrelated primary sibling cleanup must remain deferred")
			if operation.Rel != "reclaim/bucket/leaf" || operation.Direction != WriteShrink {
				continue
			}
			sourceDrainPhases++
			if !operation.Target.CPUs.Intersection(machine.MustParse(pendingCPUs)).IsEmpty() {
				sawPartialSourceDrain = true
			}
		}
	}
	require.Greater(t, sourceDrainPhases, 1, "pending source must drain in staged rounds")
	require.True(t, sawPartialSourceDrain, "trace must retain an intermediate partial source drain")
	require.True(t, trace.FinalSnapshot.DomainUnion[DomainReclaim].
		Intersection(machine.MustParse(pendingCPUs)).IsEmpty())
}

func TestCompileFixedPointTraceScalesAcross1024CPUShapes(t *testing.T) {
	t.Parallel()
	requireTopologyScaleTests(t)

	for _, depth := range []int{4, 8, 16} {
		depth := depth
		t.Run(fmt.Sprintf("depth=%d", depth), func(t *testing.T) {
			t.Parallel()
			fixture := newTraceBenchmarkFixture(t, 100, depth, 1024)
			trace, err := fixture.compile()
			require.NoError(t, err)
			require.True(t, trace.FinalEvaluation.ParentSafety.Safe)
			require.Equal(t, 100, len(trace.FinalSnapshot.Entries))
			require.Contains(t, trace.EvaluationInput.AllowedCPUs.ToSliceInt(), 1023)
			require.NotEmpty(t, flattenTraceOperations(trace))
		})
	}
}

func TestCompileFixedPointTraceHandlesTenThousandRelationsWithinBudget(t *testing.T) {
	t.Parallel()
	requireTopologyScaleTests(t)

	const nodes = 10000
	fixture := newTraceBenchmarkFixture(t, nodes, 16, benchmarkTraceCPUs)
	trace, err := fixture.compile()
	require.NoError(t, err)
	require.True(t, trace.FinalEvaluation.ParentSafety.Safe)
	require.Equal(t, nodes, len(trace.FinalSnapshot.Entries))
	require.LessOrEqual(t, len(flattenTraceOperations(trace)), nodes)
	require.Zero(t, fixture.driver.readCount())
	require.Zero(t, fixture.driver.writeCount())
}

func TestCompileFixedPointTracePerformsNoPhysicalHierarchyIO(t *testing.T) {
	t.Parallel()
	fixture := newTraceBenchmarkFixture(t, 100, 8, 1024)
	fixture.driver.resetCounts()

	trace, err := fixture.compile()
	require.NoError(t, err)
	require.NotEmpty(t, flattenTraceOperations(trace))
	require.Zero(t, fixture.driver.readCount())
	require.Zero(t, fixture.driver.writeCount())
}

func TestCompileFixedPointTraceLivePendingShape(t *testing.T) {
	t.Parallel()
	const (
		controlledSpecCount      = 17
		dynamicDescendantCount   = 177
		controlledAncestorCount  = 5
		requiredFloorRepairCount = 2
	)
	fixture := newLivePendingShapeFixture(t)

	trace, err := fixture.compile()

	require.NoError(t, err)
	require.Len(t, trace.EvaluationInput.DAGSpecs, controlledSpecCount)
	require.Len(t, trace.FinalSnapshot.Entries, controlledSpecCount+dynamicDescendantCount)
	require.LessOrEqual(t, trace.OperationCount(),
		controlledAncestorCount+requiredFloorRepairCount)
	require.True(t, trace.FinalEvaluation.ParentSafety.Safe)
	require.Zero(t, fixture.driver.writeCount())
}

func newLivePendingShapeFixture(t testing.TB) *traceBenchmarkFixture {
	t.Helper()
	const (
		primaryRoots     = 12
		controlledDepth  = 5
		dynamicPrimary   = 176
		liveShapeEntries = 194
	)
	capabilities := cgroupV2Policy.capabilities(true)
	driver := &traceBenchmarkDriver{
		nodes:        make(map[string]EntryState, liveShapeEntries),
		children:     make(map[string][]ChildRef, liveShapeEntries),
		capabilities: capabilities,
	}
	specs := make([]NodeSpec, 0, 17)
	targetByRel := make(map[string]machine.CPUSet, liveShapeEntries)
	dynamicByRel := make(map[string]machine.CPUSet, dynamicPrimary+1)
	pendingRequiredByRel := make(map[string]machine.CPUSet, controlledDepth)
	nextInode := uint64(1)
	add := func(rel, parent string, role TopoNodeRole, domain DomainID, observed machine.CPUSet, controlled bool) {
		identity := CgroupIdentity{Device: 1, Inode: nextInode}
		nextInode++
		driver.nodes[rel] = EntryState{
			Rel: rel, Identity: identity,
			CPUs: observed.Clone(), ConfiguredCPUs: observed.Clone(),
			Mems: "0", ConfiguredMems: "0",
		}
		if parent == "" {
			driver.roots = append(driver.roots, RootRef{Rel: rel, Identity: identity})
		} else {
			driver.children[parent] = append(driver.children[parent], ChildRef{
				Name: filepath.Base(rel), Identity: identity,
			})
		}
		if controlled {
			specs = append(specs, NodeSpec{
				Rel: rel, ParentRel: parent, Role: role, Domain: domain,
				CPUs: observed.Clone(), Mems: "0", TrustAnchor: parent == "",
			})
		}
		targetByRel[rel] = observed.Clone()
	}
	setControlledTarget := func(rel string, target machine.CPUSet) {
		targetByRel[rel] = target.Clone()
		for i := range specs {
			if specs[i].Rel == rel {
				specs[i].CPUs = target.Clone()
				return
			}
		}
		t.Fatalf("controlled rel %q has no spec", rel)
	}

	primaryObserved := machine.MustParse("1-6")
	primaryTarget := machine.MustParse("0-6")
	for index := 0; index < primaryRoots; index++ {
		rel := fmt.Sprintf("primary-%02d", index)
		add(rel, "", TopoNodeRolePrimary, DomainPrimary, primaryObserved, true)
	}
	parent := "primary-00"
	setControlledTarget(parent, primaryTarget)
	pendingRequiredByRel[parent] = machine.NewCPUSet(0)
	for level := 1; level < controlledDepth; level++ {
		rel := fmt.Sprintf("%s/level-%02d", parent, level)
		add(rel, parent, TopoNodeRolePrimary, DomainPrimary, primaryObserved, true)
		setControlledTarget(rel, primaryTarget)
		pendingRequiredByRel[rel] = machine.NewCPUSet(0)
		parent = rel
	}
	for index := 0; index < dynamicPrimary; index++ {
		rel := fmt.Sprintf("%s/pod-%03d", parent, index)
		add(rel, parent, TopoNodeRolePrimary, DomainPrimary, primaryObserved, false)
		dynamicByRel[rel] = primaryObserved.Clone()
	}
	add("reclaim", "", TopoNodeRoleReclaim, DomainReclaim, machine.NewCPUSet(0, 7), true)
	setControlledTarget("reclaim", machine.NewCPUSet(7))
	add("reclaim/release", "reclaim", TopoNodeRoleReclaim, DomainReclaim, machine.NewCPUSet(0), false)
	targetByRel["reclaim/release"] = machine.NewCPUSet()
	dynamicByRel["reclaim/release"] = machine.NewCPUSet()

	for rel := range driver.children {
		sort.Slice(driver.children[rel], func(i, j int) bool {
			return driver.children[rel][i].Name < driver.children[rel][j].Name
		})
	}
	sort.Slice(driver.roots, func(i, j int) bool { return driver.roots[i].Rel < driver.roots[j].Rel })
	dag, err := BuildDAG(specs)
	require.NoError(t, err)
	budget := NewBudgetTracker(traceScaleBudget(liveShapeEntries, controlledDepth))
	base, err := newCompleteSnapshotSource(driver, dag, budget)(context.Background())
	require.NoError(t, err)
	driver.resetCounts()
	round := &coordinatorRound{
		objective:            ConvergenceObjectiveParentSafe,
		dag:                  dag,
		driver:               driver,
		budget:               NewBudgetTracker(traceScaleBudget(liveShapeEntries, controlledDepth)),
		selection:            DefaultDrainSelectionPolicy(),
		targetByRel:          targetByRel,
		dynamicByRel:         dynamicByRel,
		pendingRequiredByRel: pendingRequiredByRel,
		protectedPending:     machine.NewCPUSet(0),
		cpuDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
			4: {NUMANodeID: 0, SocketID: 0, CoreID: 4},
			5: {NUMANodeID: 0, SocketID: 0, CoreID: 5},
			6: {NUMANodeID: 0, SocketID: 0, CoreID: 6},
			7: {NUMANodeID: 0, SocketID: 0, CoreID: 7},
		},
		reservedCPUs:     machine.NewCPUSet(),
		blocked:          map[DomainID]machine.CPUSet{},
		maxRounds:        64,
		allowEmptyTarget: true,
	}
	return &traceBenchmarkFixture{driver: driver, round: round, base: base}
}

func requireTopologyScaleTests(t *testing.T) {
	t.Helper()
	if os.Getenv(topologyScaleTestEnv) != "1" {
		t.Skipf("set %s=1 to run high-cost topology scale tests", topologyScaleTestEnv)
	}
}

// TestFixedPointEngineUsesSamePlanSequenceForProjectedAndRecordingSessions
// proves the engine is session-agnostic: given the same starting snapshot, a
// clone-backed projected session and a live recording session must produce a
// byte-for-byte identical ordered phase trace. The engine only decides and
// sequences operations; each session is responsible for faithfully applying
// them, so parity here is the frozen-vs-live equivalence the compiler relies on.
func TestFixedPointEngineUsesSamePlanSequenceForProjectedAndRecordingSessions(t *testing.T) {
	t.Parallel()
	projectedFixture := newAdmissionTraceFixture(t)
	projectedFixture.configureStagedSMTTransferWithDynamicDescendant()
	projectedBase := projectedFixture.snapshot()
	projectedResult, err := projectedFixture.round.runFixedPointEngine(
		context.Background(),
		newProjectedTraceSession(t, projectedBase, projectedFixture.driver.Capabilities()),
	)
	require.NoError(t, err)

	recordingFixture := newAdmissionTraceFixture(t)
	recordingFixture.configureStagedSMTTransferWithDynamicDescendant()
	recordingBase := recordingFixture.snapshot()
	recordingResult, err := recordingFixture.round.runFixedPointEngine(
		context.Background(),
		newLiveTraceSession(recordingFixture.round, recordingBase),
	)
	require.NoError(t, err)

	require.Equal(t, projectedResult.Phases, recordingResult.Phases)
}

func TestFixedPointEngineSingleRoundReturnsNeutralOutcome(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	base := fixture.snapshot()
	session := newProjectedTraceSession(t, base, fixture.driver.Capabilities())

	result, err := fixture.round.runFixedPointEngine(
		context.Background(),
		session,
		fixedPointEngineSingleRound,
	)
	require.NoError(t, err)
	require.Equal(t, 1, result.Rounds)
	require.NotNil(t, result.FinalSnapshot)
	require.NotEmpty(t, result.Phases)
	require.Equal(t, result.FinalSnapshot, result.Outcome.Snapshot)
	require.Equal(t, RoundStatusConverged, result.Outcome.Status)
	require.True(t, result.ObjectiveSatisfied)
}

func TestFixedPointEngineFailsClosedWhenAllTransferCPUsRemainProtectedDuringSnapshotChurn(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureAllProtectedSwap()
	base := fixture.snapshot()
	fixture.round.maxRounds = 4
	projected := newProjectedTraceSession(t, base, fixture.driver.Capabilities())
	session := &snapshotMetadataChurnSession{
		phaseExecutionSession: projected,
		rel:                   "primary",
	}

	_, err := fixture.round.runFixedPointEngine(context.Background(), session)

	require.ErrorIs(t, err, ErrNoProgress)
	require.NotErrorIs(t, err, ErrRoundBudgetExceeded)
	var stall *ProtectedTransferStallError
	require.ErrorAs(t, err, &stall)
	require.Equal(t, machine.NewCPUSet(0, 1), stall.CPUs)
	require.Equal(t, 1, fixture.round.round)
}

func TestProjectedPhaseNoProgress(t *testing.T) {
	t.Parallel()
	hierarchy := projectedHierarchyFixture(t, v2Capabilities())
	entry := hierarchy.snapshot.Entries[projectedChildRel]
	entry.CPUs = machine.NewCPUSet(0)
	entry.ConfiguredCPUs = machine.NewCPUSet(0)
	hierarchy.snapshot.Entries[projectedChildRel] = entry
	require.NoError(t, hierarchy.settleEvidence())
	session, err := newProjectedPhaseSession(hierarchy.snapshot, hierarchy.capabilities)
	require.NoError(t, err)
	require.NoError(t, session.hierarchy.settleEvidence())
	settledID := session.hierarchy.snapshot.ID
	entry = session.hierarchy.snapshot.Entries[projectedChildRel]
	plan := PhasePlan{
		Kind: PhaseDrain,
		Operations: []PlanOperation{{
			Rel: projectedChildRel, ExpectedIdentity: entry.Identity,
			ExpectedChildren: ChildrenFingerprint(session.hierarchy.snapshot.Children[projectedChildRel]),
			ExpectedCurrent:  CPUSetTarget{CPUs: entry.CPUs.Clone(), Mems: entry.Mems},
			Target:           CPUSetTarget{CPUs: entry.CPUs.Clone(), Mems: entry.Mems},
			Direction:        WriteShrink,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID

	_, err = session.Apply(context.Background(), plan)

	require.ErrorIs(t, err, ErrNoProgress)
	var noProgress *ProjectedPhaseNoProgressError
	require.ErrorAs(t, err, &noProgress)
	require.Equal(t, settledID, noProgress.SnapshotID)
	require.Equal(t, plan.PlanID, noProgress.PlanID)
}

func TestProjectedPhaseCycle(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	base := fixture.snapshot()
	session, err := newProjectedPhaseSession(base, fixture.driver.Capabilities())
	require.NoError(t, err)
	entry := session.hierarchy.snapshot.Entries["reclaimed/leaf"]
	plan := PhasePlan{
		Kind: PhaseDrain,
		Operations: []PlanOperation{{
			Rel: "reclaimed/leaf", ExpectedIdentity: entry.Identity,
			ExpectedChildren:       ChildrenFingerprint(session.hierarchy.snapshot.Children["reclaimed/leaf"]),
			ParentRel:              "reclaimed",
			ExpectedParentIdentity: session.hierarchy.snapshot.Entries["reclaimed"].Identity,
			ExpectedCurrent:        CPUSetTarget{CPUs: entry.CPUs.Clone(), Mems: entry.Mems},
			Target:                 CPUSetTarget{CPUs: machine.NewCPUSet(), Mems: entry.Mems},
			Direction:              WriteShrink,
		}},
	}
	plan.PlanID = canonicalExecutionPlanID(plan)
	plan.Operations[0].PlanID = plan.PlanID
	session.progress[phaseProgressKey{SnapshotID: base.ID, PlanID: plan.PlanID}] = struct{}{}

	_, err = session.Apply(context.Background(), plan)

	require.ErrorIs(t, err, ErrNoProgress)
	var cycle *ProjectedPhaseCycleError
	require.ErrorAs(t, err, &cycle)
	require.Equal(t, base.ID, cycle.SnapshotID)
	require.Equal(t, plan.PlanID, cycle.PlanID)
	require.Equal(t, 1, fixture.round.round+1)
}

func TestProtectedTransferStallRequiresZeroWriteFullyProtectedEmptyBatch(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureAllProtectedSwap()
	base := fixture.snapshot()
	plan, err := fixture.round.buildPlan(context.Background(), PhaseDrain, base)
	require.NoError(t, err)
	require.Empty(t, plan.Operations)

	tests := []struct {
		name    string
		mutate  func(*PhasePlan, *coordinatorRound) []AppliedPlanOperation
		wantNil bool
	}{
		{
			name: "all protected",
			mutate: func(_ *PhasePlan, _ *coordinatorRound) []AppliedPlanOperation {
				return nil
			},
		},
		{
			name: "partially protected",
			mutate: func(_ *PhasePlan, round *coordinatorRound) []AppliedPlanOperation {
				round.protectedByRel["reclaim"] = machine.NewCPUSet()
				return nil
			},
			wantNil: true,
		},
		{
			name: "executable drain batch",
			mutate: func(plan *PhasePlan, _ *coordinatorRound) []AppliedPlanOperation {
				plan.DrainBatch[DomainPrimary] = machine.NewCPUSet(0)
				return nil
			},
			wantNil: true,
		},
		{
			name: "verified write",
			mutate: func(_ *PhasePlan, _ *coordinatorRound) []AppliedPlanOperation {
				return []AppliedPlanOperation{{Rel: "primary"}}
			},
			wantNil: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			candidate := plan
			candidate.DrainBatch = cloneDomainUnion(plan.DrainBatch)
			round := fixture.round.cloneForProjection()
			journal := tc.mutate(&candidate, round)

			got := protectedTransferStallCPUs(candidate, round, journal)

			if tc.wantNil {
				require.True(t, got.IsEmpty())
				return
			}
			require.Equal(t, machine.NewCPUSet(0, 1), got)
		})
	}
}

func TestFixedPointEngineSessionReceivesCompletePhasePlan(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	base := fixture.snapshot()
	projected := newProjectedTraceSession(t, base, fixture.driver.Capabilities())
	session := &planRecordingSession{phaseExecutionSession: projected}

	_, err := fixture.round.runFixedPointEngine(
		context.Background(),
		session,
		fixedPointEngineSingleRound,
	)
	require.NoError(t, err)
	require.NotEmpty(t, session.plans)
	for _, plan := range session.plans {
		require.NotEmpty(t, plan.PlanID)
		require.NotNil(t, plan.Base)
		require.NotEmpty(t, plan.CanonicalTargetByRel)
		require.NotEmpty(t, plan.Operations)
	}
}

type planRecordingSession struct {
	phaseExecutionSession
	plans []PhasePlan
}

func (s *planRecordingSession) Apply(ctx context.Context, plan PhasePlan) (phaseSessionApplyResult, error) {
	s.plans = append(s.plans, plan)
	return s.phaseExecutionSession.Apply(ctx, plan)
}

type firstSnapshotErrorSession struct {
	phaseExecutionSession
	err error
}

func (s *firstSnapshotErrorSession) Snapshot(context.Context) (*CompleteSnapshot, error) {
	return nil, s.err
}

func TestFixedPointEngineFirstSnapshotFailureReturnsStaleOutcome(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	base := fixture.snapshot()
	fixture.budget = NewBudgetTracker(ConvergenceBudget{MaxRounds: 2})
	fixture.round.budget = fixture.budget
	fixture.round.round = 0
	snapshotErr := errors.New("initial snapshot preflight failed")
	session := &firstSnapshotErrorSession{
		phaseExecutionSession: newProjectedTraceSession(t, base, fixture.driver.Capabilities()),
		err:                   snapshotErr,
	}

	result, err := fixture.round.runFixedPointEngine(
		context.Background(),
		session,
		fixedPointEngineSingleRound,
	)

	require.ErrorIs(t, err, snapshotErr)
	require.NotNil(t, result)
	require.Equal(t, RoundStatusStale, result.Outcome.Status)
	require.ErrorIs(t, result.Outcome.Blocker, snapshotErr)
	require.Equal(t, fixture.budget.Usage(), result.Outcome.Cost)
	require.Equal(t, 1, result.Outcome.Cost.Rounds)
	require.Equal(t, 1, result.Rounds)
}

func TestCompiledTraceMatchesFixedPointEngineTrace(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()

	compiled, err := fixture.round.compileFixedPointTrace(
		context.Background(),
		fixture.snapshot(),
	)
	require.NoError(t, err)

	recordingFixture := newAdmissionTraceFixture(t)
	recordingFixture.configureStagedSMTTransferWithDynamicDescendant()
	observed := recordingFixture.runFixedPointAgainstRecordingDriver(t)
	require.Equal(t, flattenTraceOperations(compiled), observed)
}

func TestParentSafeCompileSkipsFrozenUnavailableChild(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureMultiFrontierParentSafeDrain()
	fixture.addUnavailableDynamicChild("kubepods/besteffort/container", "0-2", "0")
	base := fixture.snapshot()
	childIdentity := fixture.driver.nodes["kubepods/besteffort/container"].identity
	wantEvidence := UnavailableChildEvidence{
		Identity: childIdentity,
		Reason:   UnavailableChildReasonControllerUnavailable,
	}
	require.Equal(t, wantEvidence, base.UnavailableChildren["kubepods/besteffort/container"])

	trace, err := fixture.round.compileFixedPointTrace(context.Background(), base)
	require.NoError(t, err)
	require.True(t, trace.FinalEvaluation.ParentSafety.Safe)
	require.Equal(t, wantEvidence, trace.InitialSnapshot.UnavailableChildren["kubepods/besteffort/container"])
}

func TestFreezePhaseTraceRejectsNoProgress(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
	require.NoError(t, err)
	trace.Phases = nil
	trace.FinalSnapshot = CloneCompleteSnapshot(trace.InitialSnapshot)
	trace.FinalEvaluation.ParentSafety.Safe = false

	_, err = FreezePhaseTrace(trace)
	require.ErrorIs(t, err, ErrNoProgress)
	require.Zero(t, fixture.driver.PhysicalWriteCount())
}

func TestCompileFixedPointTraceRejectsUnprovableRequiredFloor(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.addPrimary("kubepods", "0-3", "0")
	fixture.addReclaim("reclaimed-0", "0", "0")
	fixture.requireCPUSet("reclaimed-0", "0-3")
	fixture.setCanonicalTarget("reclaimed-0", "0-1")

	_, err := fixture.round.compileFixedPointTrace(
		context.Background(),
		fixture.snapshot(),
	)
	require.Error(t, err)
	require.Zero(t, fixture.driver.PhysicalWriteCount())
}

func TestCompileFixedPointTraceOwnsFrozenBoundary(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()

	trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())

	require.NoError(t, err)
	require.Equal(t, FrozenBoundaryVersionV1, trace.FrozenBoundary.Version)
	require.NotEmpty(t, trace.FrozenBoundary.Roots)
	require.NotEmpty(t, trace.FrozenBoundary.ControlledRels)
	require.False(t, trace.FrozenBoundary.RelevantCPUs.IsEmpty())
}

func TestCompileFixedPointTraceOwnsIdentityScopedRollbackRetirements(t *testing.T) {
	t.Parallel()
	trace, _, dynamicRel := compiledTraceWithNonControlledDynamicWrite(t)
	controlled := make(map[string]struct{}, len(trace.FrozenBoundary.ControlledRels))
	for _, rel := range trace.FrozenBoundary.ControlledRels {
		controlled[rel] = struct{}{}
	}
	relevantHolders := make(map[string]struct{}, len(trace.FrozenBoundary.RelevantCPUHolders))
	for _, rel := range trace.FrozenBoundary.RelevantCPUHolders {
		relevantHolders[rel] = struct{}{}
	}
	expectedByRel := make(map[string]CgroupIdentity)
	for _, operation := range flattenTraceOperations(trace) {
		if operation.ExpectedCurrent.CPUs.Equals(operation.Target.CPUs) &&
			(!operation.WriteMems ||
				operation.ExpectedCurrent.Mems == operation.Target.Mems) {
			continue
		}
		if _, isControlled := controlled[operation.Rel]; isControlled {
			continue
		}
		if _, isRelevantHolder := relevantHolders[operation.Rel]; !isRelevantHolder {
			continue
		}
		expectedByRel[operation.Rel] = operation.ExpectedIdentity
	}

	require.Len(t, trace.RollbackRetirements, len(expectedByRel))
	require.Contains(t, expectedByRel, dynamicRel)
	for i, retirement := range trace.RollbackRetirements {
		require.Equal(t, expectedByRel[retirement.Rel], retirement.Identity)
		require.NotEqual(t, CgroupIdentity{}, retirement.Identity)
		if i > 0 {
			require.Less(t, trace.RollbackRetirements[i-1].Rel, retirement.Rel)
		}
	}
}

func TestFreezePhaseTraceClonesValidatesAndHashesRollbackRetirements(t *testing.T) {
	t.Parallel()
	trace, _, _ := compiledTraceWithNonControlledDynamicWrite(t)
	require.NotEmpty(t, trace.RollbackRetirements)
	frozen, err := FreezePhaseTrace(trace)
	require.NoError(t, err)
	original := append(
		[]FrozenRollbackRetirement(nil), frozen.RollbackRetirements...)

	trace.RollbackRetirements[0].Rel = "tampered"

	require.Equal(t, original, frozen.RollbackRetirements)
	_, err = FreezePhaseTrace(trace)
	require.ErrorContains(t, err, "rollback retirements are not compiler-derived")

	tampered := *frozen
	tampered.TraceID = ""
	tampered.RollbackRetirements = append(
		[]FrozenRollbackRetirement(nil), frozen.RollbackRetirements...)
	tampered.RollbackRetirements[0].Identity.Inode++
	require.NotEqual(t,
		canonicalPhaseTraceID(frozen), canonicalPhaseTraceID(&tampered))
}

func TestFreezePhaseTraceRejectsUnknownFrozenBoundaryVersion(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
	require.NoError(t, err)
	trace.FrozenBoundary.Version = FrozenBoundaryVersion(99)

	_, err = FreezePhaseTrace(trace)

	require.ErrorContains(t, err, "frozen boundary version")
}

func TestFreezePhaseTraceDeepCopiesFrozenBoundary(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
	require.NoError(t, err)

	frozen, err := FreezePhaseTrace(trace)
	require.NoError(t, err)
	want := cloneFrozenBoundary(frozen.FrozenBoundary)
	trace.FrozenBoundary.Roots[0] = "changed"
	trace.FrozenBoundary.ControlledRels[0] = "changed"
	if len(trace.FrozenBoundary.RelevantCPUHolders) > 0 {
		trace.FrozenBoundary.RelevantCPUHolders[0] = "changed"
	}
	trace.FrozenBoundary.RelevantCPUs = machine.NewCPUSet(99)

	require.Equal(t, want, frozen.FrozenBoundary)
}

func TestFreezePhaseTraceIsolatedFromMutableInputs(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	fixture.configureProtectedDeferredEvaluationInputs()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
	require.NoError(t, err)

	frozen, err := FreezePhaseTrace(trace)
	require.NoError(t, err)
	originalID := frozen.TraceID
	originalInitial := CloneCompleteSnapshot(frozen.InitialSnapshot)
	originalFinal := CloneCompleteSnapshot(frozen.FinalSnapshot)
	originalOperation := frozen.Phases[0].Operations[0]
	originalCanonical := frozen.CanonicalTargetByRel["kubepods"]
	originalRequired := frozen.RequiredCPUSetByRel["kubepods"]
	originalEvaluationInput := cloneFrozenCoordinatorEvaluationInput(frozen.EvaluationInput)

	trace.InitialSnapshot.Entries["kubepods"] = EntryState{}
	trace.FinalSnapshot.Entries["kubepods"] = EntryState{}
	trace.CanonicalTargetByRel["kubepods"] = CPUSetTarget{CPUs: machine.NewCPUSet(99)}
	trace.RequiredCPUSetByRel["kubepods"] = machine.NewCPUSet(99)
	trace.Phases[0].Operations[0] = PlanOperation{}
	trace.FinalEvaluation.ParentSafety.RequiredFloorDeficit["kubepods"] = machine.NewCPUSet(99)
	trace.EvaluationInput.ProtectedPending = machine.NewCPUSet(99)
	trace.EvaluationInput.DeferredByRel["kubepods/deferred"] = machine.NewCPUSet(99)
	for rel := range trace.EvaluationInput.DeferredCleanupRels {
		delete(trace.EvaluationInput.DeferredCleanupRels, rel)
	}
	trace.EvaluationInput.DAGSpecs[0].CPUs = machine.NewCPUSet(99)

	require.Equal(t, originalID, frozen.TraceID)
	require.Equal(t, originalInitial, frozen.InitialSnapshot)
	require.Equal(t, originalFinal, frozen.FinalSnapshot)
	require.Equal(t, originalOperation, frozen.Phases[0].Operations[0])
	require.Equal(t, originalCanonical, frozen.CanonicalTargetByRel["kubepods"])
	require.Equal(t, originalRequired, frozen.RequiredCPUSetByRel["kubepods"])
	require.Equal(t, originalEvaluationInput, frozen.EvaluationInput)
	require.Empty(t, frozen.FinalEvaluation.ParentSafety.RequiredFloorDeficit)
}

func TestFreezePhaseTraceIDIsIndependentOfMapInsertionOrder(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
	require.NoError(t, err)

	reordered := *trace
	reordered.TraceID = ""
	reordered.CanonicalTargetByRel = make(map[string]CPUSetTarget, len(trace.CanonicalTargetByRel))
	reordered.RequiredCPUSetByRel = make(map[string]machine.CPUSet, len(trace.RequiredCPUSetByRel))
	rels := make([]string, 0, len(trace.CanonicalTargetByRel))
	for rel := range trace.CanonicalTargetByRel {
		rels = append(rels, rel)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(rels)))
	for _, rel := range rels {
		if target, ok := trace.CanonicalTargetByRel[rel]; ok {
			reordered.CanonicalTargetByRel[rel] = target
		}
		if required, ok := trace.RequiredCPUSetByRel[rel]; ok {
			reordered.RequiredCPUSetByRel[rel] = required
		}
	}

	frozen, err := FreezePhaseTrace(&reordered)
	require.NoError(t, err)
	require.Equal(t, trace.TraceID, frozen.TraceID)
	require.Equal(t, trace.Phases, frozen.Phases)
}

func TestFreezePhaseTraceRejectsTamperedFinalEvaluation(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
	require.NoError(t, err)

	trace.FinalEvaluation.Report.PendingToPrimary = machine.NewCPUSet(99)

	_, err = FreezePhaseTrace(trace)
	require.ErrorContains(t, err, "final evaluation")
}

func TestFreezePhaseTraceProductionEvaluationParityWithProtectedDeferredInputs(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	fixture.configureProtectedDeferredEvaluationInputs()

	trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
	require.NoError(t, err)
	require.False(t, trace.EvaluationInput.ProtectedPending.IsEmpty())
	require.NotEmpty(t, trace.EvaluationInput.DeferredByRel)
	require.NotEmpty(t, trace.EvaluationInput.DeferredCleanupRels)

	recomputed, err := trace.EvaluationInput.evaluate(trace.FinalSnapshot)
	require.NoError(t, err)
	require.Equal(
		t,
		normalizeCoordinatorSnapshotEvaluation(trace.FinalEvaluation),
		normalizeCoordinatorSnapshotEvaluation(recomputed),
	)
	require.Zero(t, fixture.driver.PhysicalWriteCount())
}

func TestFreezePhaseTraceRejectsProtectedDeferredInputTampering(t *testing.T) {
	t.Parallel()
	newTrace := func(t *testing.T) *CompiledPhaseTrace {
		t.Helper()
		fixture := newAdmissionTraceFixture(t)
		fixture.configureStagedSMTTransferWithDynamicDescendant()
		fixture.configureProtectedDeferredEvaluationInputs()
		trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
		require.NoError(t, err)
		require.NotEmpty(t, trace.EvaluationInput.DeferredCleanupRels)
		return trace
	}

	tests := []struct {
		name       string
		mutate     func(*CompiledPhaseTrace)
		wantSubstr string
	}{
		{
			name: "protected pending",
			mutate: func(trace *CompiledPhaseTrace) {
				trace.EvaluationInput.ProtectedPending = machine.NewCPUSet(4)
			},
			wantSubstr: "final evaluation",
		},
		{
			name: "deferred leaf",
			mutate: func(trace *CompiledPhaseTrace) {
				trace.EvaluationInput.DeferredByRel["kubepods/deferred"] = machine.NewCPUSet(4)
			},
			wantSubstr: "final evaluation",
		},
		{
			name: "deferred cleanup rel",
			mutate: func(trace *CompiledPhaseTrace) {
				for rel := range trace.EvaluationInput.DeferredCleanupRels {
					delete(trace.EvaluationInput.DeferredCleanupRels, rel)
				}
			},
			wantSubstr: "identity",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			trace := newTrace(t)
			tc.mutate(trace)

			_, err := FreezePhaseTrace(trace)
			require.ErrorContains(t, err, tc.wantSubstr)
		})
	}
}

func TestCanonicalPhaseTraceIDBindsFinalEvaluation(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	fixture.configureStagedSMTTransferWithDynamicDescendant()
	trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
	require.NoError(t, err)

	tampered := *trace
	tampered.FinalEvaluation = cloneCoordinatorSnapshotEvaluation(trace.FinalEvaluation)
	tampered.FinalEvaluation.Report.PendingToPrimary = machine.NewCPUSet(99)

	require.NotEqual(t, canonicalPhaseTraceID(trace), canonicalPhaseTraceID(&tampered))
}

func TestCloneForProjectionDeepCopiesCPUDetails(t *testing.T) {
	t.Parallel()
	fixture := newAdmissionTraceFixture(t)
	clone := fixture.round.cloneForProjection()

	clone.cpuDetails[0] = machine.CPUTopoInfo{NUMANodeID: 99}
	delete(clone.cpuDetails, 1)

	require.Equal(t, 0, fixture.round.cpuDetails[0].NUMANodeID)
	require.Contains(t, fixture.round.cpuDetails, 1)
}

func TestFreezePhaseTraceRejectsOrphanOrInconsistentSnapshotEvidence(t *testing.T) {
	t.Parallel()
	newTrace := func(t *testing.T) *CompiledPhaseTrace {
		t.Helper()
		fixture := newAdmissionTraceFixture(t)
		fixture.configureStagedSMTTransferWithDynamicDescendant()
		trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
		require.NoError(t, err)
		return trace
	}

	tests := []struct {
		name   string
		mutate func(*CompleteSnapshot)
	}{
		{
			name: "orphan children key",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.Children["orphan"] = []ChildRef{{
					Name: "leaf", Identity: CgroupIdentity{Device: 1, Inode: 999},
				}}
			},
		},
		{
			name: "orphan domain key",
			mutate: func(snapshot *CompleteSnapshot) {
				snapshot.DomainByRel["orphan"] = DomainPrimary
			},
		},
		{
			name: "entry missing domain",
			mutate: func(snapshot *CompleteSnapshot) {
				delete(snapshot.DomainByRel, "kubepods")
			},
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			trace := newTrace(t)
			tc.mutate(trace.InitialSnapshot)
			trace.InitialSnapshot.ID = fingerprintSnapshot(trace.InitialSnapshot)

			_, err := FreezePhaseTrace(trace)
			require.ErrorContains(t, err, "snapshot evidence")
		})
	}
}

func TestFreezePhaseTraceRejectsUnsafeFinalSnapshot(t *testing.T) {
	t.Parallel()
	newTrace := func(t *testing.T) *CompiledPhaseTrace {
		t.Helper()
		fixture := newAdmissionTraceFixture(t)
		fixture.configureStagedSMTTransferWithDynamicDescendant()
		trace, err := fixture.round.compileFixedPointTrace(context.Background(), fixture.snapshot())
		require.NoError(t, err)
		return trace
	}

	tests := []struct {
		name       string
		mutate     func(*CompiledPhaseTrace)
		wantSubstr string
	}{
		{
			name: "nil initial snapshot",
			mutate: func(trace *CompiledPhaseTrace) {
				trace.InitialSnapshot = nil
			},
			wantSubstr: "snapshot",
		},
		{
			name: "missing operation identity",
			mutate: func(trace *CompiledPhaseTrace) {
				trace.Phases[0].Operations[0].ExpectedIdentity = CgroupIdentity{}
			},
			wantSubstr: "identity",
		},
		{
			name: "expected current mismatch",
			mutate: func(trace *CompiledPhaseTrace) {
				trace.Phases[0].Operations[0].ExpectedCurrent.CPUs = machine.NewCPUSet(99)
			},
			wantSubstr: "expected current",
		},
		{
			name: "objective not satisfied",
			mutate: func(trace *CompiledPhaseTrace) {
				trace.FinalEvaluation.ParentSafety.Safe = false
			},
			wantSubstr: "objective",
		},
		{
			name: "required CPUs absent",
			mutate: func(trace *CompiledPhaseTrace) {
				trace.RequiredCPUSetByRel["kubepods"] = machine.NewCPUSet(99)
			},
			wantSubstr: "required CPUs",
		},
		{
			name: "zero progress before objective",
			mutate: func(trace *CompiledPhaseTrace) {
				trace.Phases = nil
				trace.FinalSnapshot = CloneCompleteSnapshot(trace.InitialSnapshot)
				trace.FinalEvaluation.ParentSafety.Safe = false
			},
			wantSubstr: "no progress",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			trace := newTrace(t)
			tc.mutate(trace)
			_, err := FreezePhaseTrace(trace)
			require.ErrorContains(t, err, tc.wantSubstr)
		})
	}
}
