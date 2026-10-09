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

package bulkhead

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

func TestShouldProjectPendingToDesiredNonReclaim(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		mode          cpusetutil.CPUSetAdjustmentMode
		hardPartition bool
		allowOverlap  bool
		want          bool
	}{
		{name: "admission soft non-overlap", mode: cpusetutil.CPUSetAdjustmentModeAdmission, want: true},
		// RampUp hard-partition floor active but non-overlap: pending shared ramp-up
		// projection must STILL engage. The old !hardPartition conjunct forced this
		// false, which deadlocked the very admission the ramp-up floor exists to
		// protect (sa8). Only share/reclaim overlap disables projection.
		{name: "admission hard-partition non-overlap projects", mode: cpusetutil.CPUSetAdjustmentModeAdmission, hardPartition: true, want: true},
		{name: "periodic round", mode: cpusetutil.CPUSetAdjustmentModePeriodic, want: false},
		// Retry stays gate-false here by design; the manager-owned AppliedView floor
		// is re-engaged separately in the topology plugin
		// (shouldProjectPendingToValidatedAuthority), not by this admission gate.
		{name: "retry round stays gate-false", mode: cpusetutil.CPUSetAdjustmentModeRetry, want: false},
		{name: "overlap", mode: cpusetutil.CPUSetAdjustmentModeAdmission, allowOverlap: true, want: false},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := shouldProjectPendingToDesiredNonReclaim(tt.mode, tt.hardPartition, tt.allowOverlap); got != tt.want {
				t.Fatalf("shouldProjectPendingToDesiredNonReclaim() = %t, want %t", got, tt.want)
			}
		})
	}
}

type fakePlugin struct {
	name                   string
	adjustViews            []*model.DesiredView
	adjustOwnedViews       []*model.CPUSetPartitionView
	adjustApplied          []*model.AppliedView
	adjustRevision         []uint64
	periodicCalls          int
	periodicStates         []interface{}
	periodicApplied        []*model.AppliedView
	periodicRevision       []uint64
	periodicValid          []bool
	disabledCalls          int
	enableStates           []interface{}
	enabled                bool
	adjustErr              error
	periodicErr            error
	disabledErr            error
	disabledErrs           []error
	adjustStarted          chan struct{}
	adjustRelease          chan struct{}
	periodicWaitForContext bool
	periodicContextErr     error
	disabledDeadlines      []time.Time
}

type capturedMetric struct {
	key      string
	val      float64
	emitType metrics.MetricTypeName
	tags     []metrics.MetricTag
}

type capturingEmitter struct {
	records []capturedMetric
}

func (e *capturingEmitter) StoreInt64(key string, val int64, emitType metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	e.records = append(e.records, capturedMetric{
		key:      key,
		val:      float64(val),
		emitType: emitType,
		tags:     append([]metrics.MetricTag(nil), tags...),
	})
	return nil
}

func (e *capturingEmitter) StoreFloat64(key string, val float64, emitType metrics.MetricTypeName, tags ...metrics.MetricTag) error {
	e.records = append(e.records, capturedMetric{
		key:      key,
		val:      val,
		emitType: emitType,
		tags:     append([]metrics.MetricTag(nil), tags...),
	})
	return nil
}

func (e *capturingEmitter) WithTags(string, ...metrics.MetricTag) metrics.MetricEmitter {
	return e
}

func (e *capturingEmitter) Run(context.Context) {}

func (p *fakePlugin) Name() string { return p.name }

func (p *fakePlugin) Enable(in bulkheadapi.HandlerContext) bool {
	p.enableStates = append(p.enableStates, in.State)
	return p.enabled
}

func (p *fakePlugin) CPUSetAdjustmentHandler(ctx context.Context, in bulkheadapi.HandlerContext) error {
	p.adjustViews = append(p.adjustViews, in.DesiredView)
	p.adjustOwnedViews = append(p.adjustOwnedViews, in.View)
	p.adjustApplied = append(p.adjustApplied, in.AppliedView)
	p.adjustRevision = append(p.adjustRevision, in.AppliedViewRevision)
	if p.adjustStarted != nil {
		close(p.adjustStarted)
	}
	if p.adjustRelease != nil {
		select {
		case <-p.adjustRelease:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return p.adjustErr
}

type fakeTopologyPlugin struct {
	*fakePlugin
	result        fakeDAGResult
	err           error
	afterApply    func()
	mutateDesired func(*model.DesiredView)
	sawApplied    []*model.AppliedView
	sawRevision   []uint64
}

type fakeDisabledTopologyPlugin struct {
	*fakePlugin
	shouldReconcile     bool
	reconcileCalls      int
	results             []fakeDAGResult
	reconcileErrs       []error
	deadlines           []time.Time
	resetComplete       bool
	lastEnabled         bool
	lastShouldReconcile bool
}

type lockObservedContext struct {
	context.Context
	observed chan struct{}
	once     sync.Once
}

func (c *lockObservedContext) Done() <-chan struct{} {
	c.once.Do(func() {
		close(c.observed)
	})
	return c.Context.Done()
}

type fakeFullAndDisabledTopologyPlugin struct {
	*fakeDisabledTopologyPlugin
	applyCalls   int
	applyResults []fakeDAGResult
	applyErrs    []error
}

func (p *fakeDisabledTopologyPlugin) ShouldReconcileWhenDisabled(
	_ context.Context,
	_ bulkheadapi.HandlerContext,
) bool {
	if p.lastShouldReconcile != p.shouldReconcile {
		p.resetComplete = false
	}
	p.lastShouldReconcile = p.shouldReconcile
	return p.shouldReconcile
}

func (p *fakeDisabledTopologyPlugin) ReconcileDisabled(
	ctx context.Context,
	_ bulkheadapi.HandlerContext,
) (bulkheadapi.TopologyOutcome, error) {
	p.reconcileCalls++
	if deadline, ok := ctx.Deadline(); ok {
		p.deadlines = append(p.deadlines, deadline)
	}
	index := p.reconcileCalls - 1
	var result fakeDAGResult
	if index < len(p.results) {
		result = p.results[index]
	}
	var err error
	if index < len(p.reconcileErrs) {
		err = p.reconcileErrs[index]
	}
	return result.outcome(true), err
}

func (p *fakeDisabledTopologyPlugin) Enable(in bulkheadapi.HandlerContext) bool {
	curr := p.fakePlugin.Enable(in)
	if p.lastEnabled && !curr {
		p.resetComplete = false
	}
	p.lastEnabled = curr
	return curr
}

func (p *fakeDisabledTopologyPlugin) NeedsDisabledReset() bool { return !p.resetComplete }

func (p *fakeDisabledTopologyPlugin) CPUSetAdjustmentDisabledHandler(ctx context.Context, in bulkheadapi.HandlerContext) error {
	return p.fakePlugin.CPUSetAdjustmentDisabledHandler(ctx, in)
}

func (p *fakeDisabledTopologyPlugin) MarkDisabledResetComplete() { p.resetComplete = true }

func (p *fakeFullAndDisabledTopologyPlugin) Apply(
	_ context.Context,
	_ bulkheadapi.HandlerContext,
) (bulkheadapi.TopologyOutcome, error) {
	p.applyCalls++
	index := p.applyCalls - 1
	var result fakeDAGResult
	if index < len(p.applyResults) {
		result = p.applyResults[index]
	}
	var err error
	if index < len(p.applyErrs) {
		err = p.applyErrs[index]
	}
	return result.outcome(false), err
}

func (p *fakeTopologyPlugin) Apply(_ context.Context, in bulkheadapi.HandlerContext) (bulkheadapi.TopologyOutcome, error) {
	p.sawApplied = append(p.sawApplied, in.AppliedView)
	p.sawRevision = append(p.sawRevision, in.AppliedViewRevision)
	if p.mutateDesired != nil {
		p.mutateDesired(in.DesiredView)
	}
	if p.afterApply != nil {
		p.afterApply()
	}
	return p.result.outcome(false), p.err
}

type fakeDAGResult struct {
	FullyConverged       bool
	ParentSafe           bool
	FinalSnapshotCurrent bool
	AppliedView          *model.AppliedView
	DeferredLeafCount    int // ignored; recorded for fidelity, classification uses the booleans
}

func (r fakeDAGResult) outcome(requireReclaimOnly bool) bulkheadapi.TopologyOutcome {
	if requireReclaimOnly {
		if !r.FullyConverged || !r.FinalSnapshotCurrent || r.AppliedView == nil ||
			r.AppliedView.Level != model.AppliedViewLevelReclaimOnly {
			return bulkheadapi.TopologyOutcome{Level: bulkheadapi.ConvergenceLevelNone}
		}
		return bulkheadapi.TopologyOutcome{
			Level:   bulkheadapi.ConvergenceLevelFull,
			View:    r.AppliedView,
			Reclaim: r.AppliedView.ReclaimEffective.Clone(),
		}
	}
	successful := r.FullyConverged || r.ParentSafe
	if !successful || !r.FinalSnapshotCurrent || r.AppliedView == nil {
		return bulkheadapi.TopologyOutcome{Level: bulkheadapi.ConvergenceLevelNone}
	}
	level := bulkheadapi.ConvergenceLevelFull
	if r.ParentSafe && !r.FullyConverged {
		level = bulkheadapi.ConvergenceLevelPartial
	}
	return bulkheadapi.TopologyOutcome{
		Level:   level,
		View:    r.AppliedView,
		Reclaim: r.AppliedView.ReclaimEffective.Clone(),
	}
}

func reclaimOnlyResult(cpus machine.CPUSet) fakeDAGResult {
	view := &model.AppliedView{
		CPUSetPartitionView: model.NewCPUSetPartitionView(),
		Level:               model.AppliedViewLevelReclaimOnly,
		CPUSetByRel:         map[string]machine.CPUSet{},
		RelProofByRel:       map[string]model.CgroupRelProof{},
	}
	view.ReclaimEffective = cpus.Clone()
	return fakeDAGResult{
		FullyConverged:       true,
		FinalSnapshotCurrent: true,
		AppliedView:          view,
	}
}

func TestManagerReconcilesDisabledTopologyEveryRoundAndResetsOnce(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results: []fakeDAGResult{
			reclaimOnlyResult(machine.NewCPUSet(1, 2)),
			reclaimOnlyResult(machine.NewCPUSet(2, 3)),
		},
	}
	dependent := &fakePlugin{name: "dependent", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	in := enabledCPUSetAdjustmentCtx()

	first, err := m.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("first Apply() error: %v", err)
	}
	second, err := m.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("second Apply() error: %v", err)
	}

	assertCPUSet(t, "first applied reclaim", first, "1-2")
	assertCPUSet(t, "second applied reclaim", second, "2-3")
	if topologyPlugin.disabledCalls != 1 {
		t.Fatalf("disabled reset calls = %d, want 1", topologyPlugin.disabledCalls)
	}
	if topologyPlugin.reconcileCalls != 2 {
		t.Fatalf("disabled reconcile calls = %d, want 2", topologyPlugin.reconcileCalls)
	}
	if len(dependent.adjustViews) != 2 {
		t.Fatalf("dependent adjustment calls = %d, want 2", len(dependent.adjustViews))
	}
	if m.appliedView == nil || m.appliedView.Level != model.AppliedViewLevelReclaimOnly {
		t.Fatalf("published applied view = %+v, want reclaim-only", m.appliedView)
	}
	assertCPUSet(t, "latest applied reclaim", m.LatestAppliedReclaim(), "2-3")
}

func TestManagerRetainsDisabledResetAfterReconcileFailure(t *testing.T) {
	t.Parallel()

	reconcileErr := errors.New("reconcile failed")
	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results: []fakeDAGResult{
			{},
			reclaimOnlyResult(machine.NewCPUSet(1)),
		},
		reconcileErrs: []error{reconcileErr},
	}
	dependent := &fakePlugin{name: "dependent", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	in := enabledCPUSetAdjustmentCtx()

	if _, err := m.Apply(context.Background(), in); !errors.Is(err, reconcileErr) {
		t.Fatalf("first Apply() error = %v, want %v", err, reconcileErr)
	}
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("second Apply() error: %v", err)
	}

	if topologyPlugin.disabledCalls != 1 {
		t.Fatalf("disabled reset calls = %d, want 1 after reconcile retry", topologyPlugin.disabledCalls)
	}
	if topologyPlugin.reconcileCalls != 2 {
		t.Fatalf("disabled reconcile calls = %d, want 2", topologyPlugin.reconcileCalls)
	}
	if len(dependent.adjustViews) != 1 {
		t.Fatalf("dependent adjustment calls = %d, want 1 after successful retry", len(dependent.adjustViews))
	}
}

func TestManagerRetriesInitialDisabledResetFailure(t *testing.T) {
	t.Parallel()

	resetErr := errors.New("initial reset failed")
	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin: &fakePlugin{
			name:         "cpuset_topology",
			disabledErrs: []error{resetErr, nil},
		},
		shouldReconcile: true,
		results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet(1))},
	}
	dependent := &fakePlugin{name: "dependent", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	in := enabledCPUSetAdjustmentCtx()

	if _, err := m.Apply(context.Background(), in); !errors.Is(err, resetErr) {
		t.Fatalf("first Apply() error = %v, want %v", err, resetErr)
	}
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("retried Apply() error: %v", err)
	}
	if topologyPlugin.disabledCalls != 2 {
		t.Fatalf("disabled reset calls = %d, want failed reset and retry", topologyPlugin.disabledCalls)
	}
	if topologyPlugin.reconcileCalls != 1 || len(dependent.adjustViews) != 1 {
		t.Fatalf("reconcile calls=%d dependent calls=%d, want one each after successful reset",
			topologyPlugin.reconcileCalls, len(dependent.adjustViews))
	}
}

func TestManagerRetriesPendingResetWhenReclaimOnlyBecomesIneligibleAfterFailure(t *testing.T) {
	t.Parallel()

	resetErr := errors.New("reclaim-only entry reset failed")
	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin: &fakePlugin{
			name:         "cpuset_topology",
			disabledErrs: []error{nil, resetErr, nil},
		},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("initial ordinary disabled Apply() error: %v", err)
	}
	topologyPlugin.shouldReconcile = true
	if _, err := m.Apply(context.Background(), in); !errors.Is(err, resetErr) {
		t.Fatalf("reclaim-only entry Apply() error = %v, want %v", err, resetErr)
	}
	topologyPlugin.shouldReconcile = false
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("ordinary disabled Apply() after failed reclaim-only entry error: %v", err)
	}
	if topologyPlugin.disabledCalls != 3 {
		t.Fatalf("disabled reset calls = %d, want initial reset, failed reclaim-only entry, and authoritative retry",
			topologyPlugin.disabledCalls)
	}
}

func TestManagerRetriesPendingResetWhenReclaimOnlyBecomesIneligibleAfterStaleFence(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology"},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("initial ordinary disabled Apply() error: %v", err)
	}
	topologyPlugin.shouldReconcile = true
	staleOnce := true
	in.CommitIfGenerationCurrent = func(_ uint64, commit func()) bool {
		if topologyPlugin.disabledCalls == 2 && staleOnce {
			staleOnce = false
			return false
		}
		commit()
		return true
	}
	var nonConverged *NonConvergedError
	if _, err := m.Apply(context.Background(), in); !errors.As(err, &nonConverged) {
		t.Fatalf("stale reclaim-only entry Apply() error = %v, want NonConvergedError", err)
	}
	topologyPlugin.shouldReconcile = false
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("ordinary disabled Apply() after stale reclaim-only entry error: %v", err)
	}
	if topologyPlugin.disabledCalls != 3 {
		t.Fatalf("disabled reset calls = %d, want initial reset, stale reclaim-only entry, and authoritative retry",
			topologyPlugin.disabledCalls)
	}
}

func TestManagerRejectsStaleReclaimOnlySnapshotBeforePublishing(t *testing.T) {
	t.Parallel()

	result := reclaimOnlyResult(machine.NewCPUSet(1))
	result.FinalSnapshotCurrent = false
	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results:         []fakeDAGResult{result},
	}
	dependent := &fakePlugin{name: "dependent", enabled: true}
	old := reclaimOnlyResult(machine.NewCPUSet(3)).AppliedView
	m := &Manager{
		plugins:             []bulkheadapi.Plugin{topologyPlugin, dependent},
		appliedView:         old,
		appliedViewRevision: 7,
	}
	m.publishLatestAppliedReclaim(old.ReclaimEffective)

	_, err := m.Apply(context.Background(), enabledCPUSetAdjustmentCtx())
	var nonConverged *NonConvergedError
	if !errors.As(err, &nonConverged) {
		t.Fatalf("Apply() error = %v, want NonConvergedError", err)
	}
	if len(dependent.adjustViews) != 0 {
		t.Fatalf("dependent calls = %d, want 0", len(dependent.adjustViews))
	}
	if m.appliedViewRevision != 7 {
		t.Fatalf("applied revision = %d, want 7", m.appliedViewRevision)
	}
	assertCPUSet(t, "retained latest reclaim", m.LatestAppliedReclaim(), "3")
}

func TestManagerRetriesDisabledResetAfterStaleGenerationFence(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet(1))},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()
	staleOnce := true
	in.CommitIfGenerationCurrent = func(_ uint64, commit func()) bool {
		if topologyPlugin.disabledCalls == 1 && staleOnce {
			staleOnce = false
			return false
		}
		commit()
		return true
	}

	var nonConverged *NonConvergedError
	if _, err := m.Apply(context.Background(), in); !errors.As(err, &nonConverged) {
		t.Fatalf("stale reset Apply() error = %v, want NonConvergedError", err)
	}
	in.CommitIfGenerationCurrent = nil
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("retried Apply() error: %v", err)
	}
	if topologyPlugin.disabledCalls != 2 {
		t.Fatalf("disabled reset calls = %d, want stale reset retried", topologyPlugin.disabledCalls)
	}
}

func TestManagerRejectsStaleGenerationAfterReclaimOnlyConvergence(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet(1))},
	}
	dependent := &fakePlugin{name: "dependent", enabled: true}
	old := reclaimOnlyResult(machine.NewCPUSet(3)).AppliedView
	m := &Manager{
		plugins:             []bulkheadapi.Plugin{topologyPlugin, dependent},
		appliedView:         old,
		appliedViewRevision: 7,
	}
	m.publishLatestAppliedReclaim(old.ReclaimEffective)
	in := enabledCPUSetAdjustmentCtx()
	fenceCalls := 0
	in.CommitIfGenerationCurrent = func(_ uint64, commit func()) bool {
		fenceCalls++
		if fenceCalls == 4 {
			return false
		}
		commit()
		return true
	}

	var nonConverged *NonConvergedError
	if _, err := m.Apply(context.Background(), in); !errors.As(err, &nonConverged) {
		t.Fatalf("Apply() error = %v, want stale generation NonConvergedError", err)
	}
	if len(dependent.adjustViews) != 0 {
		t.Fatalf("dependent calls = %d, want 0 before stale publication", len(dependent.adjustViews))
	}
	if m.appliedViewRevision != 7 {
		t.Fatalf("applied revision = %d, want 7", m.appliedViewRevision)
	}
	assertCPUSet(t, "retained latest reclaim", m.LatestAppliedReclaim(), "3")
}

func TestManagerEmptyReclaimOnlyResultDoesNotWriteCommitOverride(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet())},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	m.publishLatestAppliedReclaim(machine.NewCPUSet(3))
	override := &cpusetutil.CPUSetAdjustmentCommitOverride{}
	in := enabledCPUSetAdjustmentCtx()
	in.CommitOverride = override

	got, err := m.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if !got.IsEmpty() || !m.LatestAppliedReclaim().IsEmpty() {
		t.Fatalf("empty reclaim result not published: return=%s latest=%s", got.String(), m.LatestAppliedReclaim().String())
	}
	if override.Source != "" || !override.ReclaimEffective.IsEmpty() {
		t.Fatalf("empty reclaim result wrote commit override: %+v", override)
	}
}

func TestManagerHardPartitionAcceptsEmptyReclaimOnlyResultWithoutCommitOverride(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 2, 2)
	if err != nil {
		t.Fatalf("GenerateDummyCPUTopology() error: %v", err)
	}
	state := cpustate.NewCPUPluginState(topology)
	state.SetAllocationInfo(
		commonstate.PoolNameReserve,
		commonstate.FakedContainerName,
		&cpustate.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
			AllocationResult: machine.NewCPUSet(),
		},
	)
	state.SetAllocationInfo(
		commonstate.PoolNameReclaim,
		commonstate.FakedContainerName,
		&cpustate.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: topology.CPUDetails.CPUs(),
		},
	)
	state.SetAllocationInfo("ramp-up-pod", "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "ramp-up-pod",
			ContainerName: "main",
		},
		RampUp: true,
	})

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet())},
	}
	manager := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	override := &cpusetutil.CPUSetAdjustmentCommitOverride{}
	dynamicConf := dynamicBulkheadConf(true)
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.InitialRampUpReclaimCPUSetRatio = 0.2

	got, err := manager.Apply(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf:    dynamicConf,
		State:          state,
		Topology:       topology,
		CommitOverride: override,
	})
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if !got.IsEmpty() || !manager.LatestAppliedReclaim().IsEmpty() {
		t.Fatalf("empty reclaim-only result not retained: return=%s latest=%s",
			got.String(), manager.LatestAppliedReclaim().String())
	}
	if override.Source != "" || !override.ReclaimEffective.IsEmpty() {
		t.Fatalf("empty reclaim-only result wrote commit override: %+v", override)
	}
}

func TestManagerValidateAppliedHardPartitionEmptyAppliedView(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 2, 2)
	if err != nil {
		t.Fatalf("GenerateDummyCPUTopology() error: %v", err)
	}
	state := cpustate.NewCPUPluginState(topology)
	state.SetAllocationInfo(
		commonstate.PoolNameReserve,
		commonstate.FakedContainerName,
		&cpustate.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
			AllocationResult: machine.NewCPUSet(),
		},
	)
	state.SetAllocationInfo(
		commonstate.PoolNameReclaim,
		commonstate.FakedContainerName,
		&cpustate.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: topology.CPUDetails.CPUs(),
		},
	)
	state.SetAllocationInfo("ramp-up-pod", "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "ramp-up-pod",
			ContainerName: "main",
		},
		RampUp: true,
	})
	dynamicConf := dynamicBulkheadConf(true)
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.InitialRampUpReclaimCPUSetRatio = 0.2
	in := cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicConf,
		State:       state,
		Topology:    topology,
	}

	for _, tc := range []struct {
		name    string
		level   model.AppliedViewLevel
		wantErr bool
	}{
		{name: "reclaim-only reset sentinel", level: model.AppliedViewLevelReclaimOnly},
		{name: "full view fails closed", level: model.AppliedViewLevelFull, wantErr: true},
		{name: "parent-safe view fails closed", level: model.AppliedViewLevelParentSafe, wantErr: true},
	} {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			applied := model.NewDesiredView().ToAppliedView()
			applied.Level = tc.level

			err := (&Manager{}).validateAppliedHardPartition(in, applied)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validateAppliedHardPartition() error = %v, wantErr %t", err, tc.wantErr)
			}
		})
	}
}

func TestManagerRejectsInvalidReclaimOnlyResult(t *testing.T) {
	t.Parallel()

	result := reclaimOnlyResult(machine.NewCPUSet(1))
	result.AppliedView.Level = model.AppliedViewLevelParentSafe
	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results:         []fakeDAGResult{result},
	}
	dependent := &fakePlugin{name: "dependent", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}

	_, err := m.Apply(context.Background(), enabledCPUSetAdjustmentCtx())
	var nonConverged *NonConvergedError
	if !errors.As(err, &nonConverged) {
		t.Fatalf("Apply() error = %v, want NonConvergedError", err)
	}
	if len(dependent.adjustViews) != 0 {
		t.Fatalf("dependent adjustment calls = %d, want 0", len(dependent.adjustViews))
	}
}

func TestManagerDisabledResetAndReconcileShareDeadline(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet(1))},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()
	in.CoreConf = config.NewConfiguration()
	in.CoreConf.CPUQRMPluginConfig.BulkheadConfiguration.TopologyConvergenceBudget.DeadlineDuration = time.Second

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if len(topologyPlugin.disabledDeadlines) != 1 || len(topologyPlugin.deadlines) != 1 {
		t.Fatalf("deadline observations reset=%v reconcile=%v, want one each",
			topologyPlugin.disabledDeadlines, topologyPlugin.deadlines)
	}
	if !topologyPlugin.disabledDeadlines[0].Equal(topologyPlugin.deadlines[0]) {
		t.Fatalf("reset deadline %s differs from reconcile deadline %s",
			topologyPlugin.disabledDeadlines[0], topologyPlugin.deadlines[0])
	}
}

func TestManagerDisabledDeadlineStartsAfterLockAcquisition(t *testing.T) {
	t.Parallel()
	const deadlineDuration = 80 * time.Millisecond

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet(1))},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()
	in.CoreConf = config.NewConfiguration()
	in.CoreConf.CPUQRMPluginConfig.BulkheadConfiguration.TopologyConvergenceBudget.DeadlineDuration = deadlineDuration

	if err := m.mu.Lock(context.Background()); err != nil {
		t.Fatalf("hold manager lock: %v", err)
	}
	observed := make(chan struct{})
	ctx := &lockObservedContext{Context: context.Background(), observed: observed}
	done := make(chan error, 1)
	go func() {
		_, err := m.Apply(ctx, in)
		done <- err
	}()
	<-observed
	time.Sleep(2 * deadlineDuration)
	lockReleasedAt := time.Now()
	m.mu.Unlock()

	if err := <-done; err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if len(topologyPlugin.deadlines) != 1 {
		t.Fatalf("reconcile deadlines = %v, want one", topologyPlugin.deadlines)
	}
	if remaining := topologyPlugin.deadlines[0].Sub(lockReleasedAt); remaining < deadlineDuration/2 {
		t.Fatalf("deadline remaining after lock acquisition = %s, want at least %s",
			remaining, deadlineDuration/2)
	}
}

func TestManagerClearsDisabledResetCompletionAfterEnabledRound(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results: []fakeDAGResult{
			reclaimOnlyResult(machine.NewCPUSet(1)),
			reclaimOnlyResult(machine.NewCPUSet(1)),
		},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("first disabled Apply() error: %v", err)
	}
	topologyPlugin.enabled = true
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("enabled Apply() error: %v", err)
	}
	topologyPlugin.enabled = false
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("second disabled Apply() error: %v", err)
	}
	if topologyPlugin.disabledCalls != 2 {
		t.Fatalf("disabled reset calls = %d, want 2 across disabled epochs", topologyPlugin.disabledCalls)
	}
}

func TestManagerLeavingDisabledReconcileRunsAuthoritativeDisabledReset(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet(1))},
	}
	dependent := &fakePlugin{name: "dependent", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	in := enabledCPUSetAdjustmentCtx()

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("reclaim-only Apply() error: %v", err)
	}
	topologyPlugin.shouldReconcile = false
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("ordinary disabled Apply() error: %v", err)
	}
	if topologyPlugin.disabledCalls != 2 {
		t.Fatalf("disabled reset calls = %d, want reclaim-only transition and authoritative exit reset", topologyPlugin.disabledCalls)
	}
	if len(dependent.adjustViews) != 1 {
		t.Fatalf("dependent adjustment calls = %d, want only reclaim-only round", len(dependent.adjustViews))
	}
}

func TestManagerLeavingDisabledReconcileRetriesFailedReset(t *testing.T) {
	t.Parallel()

	resetErr := errors.New("exit reset failed")
	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin: &fakePlugin{
			name:         "cpuset_topology",
			disabledErrs: []error{nil, resetErr, nil},
		},
		shouldReconcile: true,
		results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet(1))},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("reclaim-only Apply() error: %v", err)
	}
	topologyPlugin.shouldReconcile = false
	if _, err := m.Apply(context.Background(), in); !errors.Is(err, resetErr) {
		t.Fatalf("failed exit Apply() error = %v, want %v", err, resetErr)
	}
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("retried exit Apply() error: %v", err)
	}
	if topologyPlugin.disabledCalls != 3 {
		t.Fatalf("disabled reset calls = %d, want transition, failed exit, and retried exit", topologyPlugin.disabledCalls)
	}
}

func TestManagerLeavingDisabledReconcileRetriesStaleReset(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology"},
		shouldReconcile: true,
		results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet(1))},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()
	staleOnce := true
	in.CommitIfGenerationCurrent = func(_ uint64, commit func()) bool {
		if topologyPlugin.disabledCalls == 2 && staleOnce {
			staleOnce = false
			return false
		}
		commit()
		return true
	}

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("reclaim-only Apply() error: %v", err)
	}
	topologyPlugin.shouldReconcile = false
	var nonConverged *NonConvergedError
	if _, err := m.Apply(context.Background(), in); !errors.As(err, &nonConverged) {
		t.Fatalf("stale exit Apply() error = %v, want NonConvergedError", err)
	}
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("retried stale exit Apply() error: %v", err)
	}
	if topologyPlugin.disabledCalls != 3 {
		t.Fatalf("disabled reset calls = %d, want transition, stale exit, and retried exit", topologyPlugin.disabledCalls)
	}
}

func TestManagerFullTopologyAttemptAfterReclaimOnlyMarksDisabledResetPending(t *testing.T) {
	t.Parallel()

	applyErr := errors.New("full topology partially wrote before failing")
	topologyPlugin := &fakeFullAndDisabledTopologyPlugin{
		fakeDisabledTopologyPlugin: &fakeDisabledTopologyPlugin{
			fakePlugin:      &fakePlugin{name: "cpuset_topology"},
			shouldReconcile: true,
			results:         []fakeDAGResult{reclaimOnlyResult(machine.NewCPUSet(1))},
		},
		applyErrs: []error{applyErr},
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("reclaim-only Apply() error: %v", err)
	}
	topologyPlugin.enabled = true
	topologyPlugin.shouldReconcile = false
	if _, err := m.Apply(context.Background(), in); !errors.Is(err, applyErr) {
		t.Fatalf("full topology Apply() error = %v, want %v", err, applyErr)
	}
	topologyPlugin.enabled = false
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("disabled Apply() after partial full attempt error: %v", err)
	}
	if topologyPlugin.disabledCalls != 2 {
		t.Fatalf("disabled reset calls = %d, want reclaim-only reset and authoritative reset after full attempt",
			topologyPlugin.disabledCalls)
	}
}

func (p *fakePlugin) PeriodicalHandler(
	ctx context.Context,
	in bulkheadapi.PeriodicalHandlerContext,
) error {
	p.periodicCalls++
	p.periodicApplied = append(p.periodicApplied, in.AppliedView)
	p.periodicRevision = append(p.periodicRevision, in.AppliedViewRevision)
	p.periodicValid = append(p.periodicValid, in.AppliedViewValidForPeriodical)
	if in.EffectiveEnabled == nil {
		p.periodicStates = append(p.periodicStates, nil)
	} else {
		p.periodicStates = append(p.periodicStates, *in.EffectiveEnabled)
	}
	if p.periodicWaitForContext {
		<-ctx.Done()
		p.periodicContextErr = ctx.Err()
		return ctx.Err()
	}
	return p.periodicErr
}

func (p *fakePlugin) CPUSetAdjustmentDisabledHandler(ctx context.Context, _ bulkheadapi.HandlerContext) error {
	p.disabledCalls++
	if deadline, ok := ctx.Deadline(); ok {
		p.disabledDeadlines = append(p.disabledDeadlines, deadline)
	}
	if index := p.disabledCalls - 1; index < len(p.disabledErrs) {
		return p.disabledErrs[index]
	}
	return p.disabledErr
}

func TestManagerApplyLockWaitIsCancelable(t *testing.T) {
	t.Parallel()
	blocking := &fakePlugin{
		name:          "blocking",
		enabled:       true,
		adjustStarted: make(chan struct{}),
		adjustRelease: make(chan struct{}),
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{blocking}}
	firstDone := make(chan error, 1)
	go func() {
		_, err := m.Apply(context.Background(), enabledCPUSetAdjustmentCtx())
		firstDone <- err
	}()
	<-blocking.adjustStarted

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := m.Apply(ctx, enabledCPUSetAdjustmentCtx())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second Apply error = %v, want context deadline while waiting for manager lock", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("canceled lock wait took %s", elapsed)
	}

	close(blocking.adjustRelease)
	if err := <-firstDone; err != nil {
		t.Fatalf("first Apply: %v", err)
	}
}

func TestManagerPeriodicalHandlerUsesBoundedContext(t *testing.T) {
	t.Parallel()
	plugin := &fakePlugin{name: "periodical", periodicWaitForContext: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}
	coreConf := config.NewConfiguration()
	coreConf.CPUQRMPluginConfig.BulkheadConfiguration.TopologyConvergenceBudget.DeadlineDuration = time.Millisecond

	started := time.Now()
	m.RunPeriodicalHandlers(coreConf, nil, enabledDynamicAgentConf(), nil, nil)
	if !errors.Is(plugin.periodicContextErr, context.DeadlineExceeded) {
		t.Fatalf("periodical context error = %v, want deadline exceeded", plugin.periodicContextErr)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded periodical handler took %s", elapsed)
	}
}

func TestManagerApplyRequiresFullyConvergedTopologyBeforeDependents(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       false,
			FinalSnapshotCurrent: true,
		},
	}
	dependent := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}

	got, err := m.Apply(context.Background(), enabledCPUSetAdjustmentCtx())
	var nonConverged *NonConvergedError
	if !errors.As(err, &nonConverged) {
		t.Fatalf("Apply() error = %v, want *NonConvergedError", err)
	}
	if !got.IsEmpty() {
		t.Fatalf("Apply() reclaim = %s, want empty on non-convergence", got.String())
	}
	if len(dependent.adjustViews) != 0 {
		t.Fatalf("dependent calls = %d, want 0", len(dependent.adjustViews))
	}
	if !m.LatestAppliedReclaim().IsEmpty() {
		t.Fatalf("non-converged apply published reclaim %s", m.LatestAppliedReclaim().String())
	}
}

func TestManagerApplyAcceptsParentSafeTopologyWithoutRunningDependents(t *testing.T) {
	t.Parallel()

	override := &cpusetutil.CPUSetAdjustmentCommitOverride{}
	applied := &model.AppliedView{
		Level: model.AppliedViewLevelParentSafe,
		CPUSetPartitionView: model.CPUSetPartitionView{
			ReclaimEffective: machine.NewCPUSet(2, 3),
		},
	}
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			ParentSafe:           true,
			DeferredLeafCount:    1,
			FinalSnapshotCurrent: true,
			AppliedView:          applied,
		},
	}
	dependent := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	in := enabledCPUSetAdjustmentCtx()
	in.CommitOverride = override

	got, err := m.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !got.Equals(machine.NewCPUSet(2, 3)) {
		t.Fatalf("Apply() reclaim = %s, want 2-3", got.String())
	}
	if len(dependent.adjustViews) != 0 {
		t.Fatalf("dependent calls = %d, want none for parent-safe view", len(dependent.adjustViews))
	}
	if m.appliedViewValidForPeriodical {
		t.Fatal("parent-safe view must not authorize periodical leaf-dependent plugins")
	}
	if !override.ReclaimEffective.IsEmpty() || override.Source != "" {
		t.Fatalf("parent-safe view published state commit override: reclaim=%s source=%q",
			override.ReclaimEffective.String(), override.Source)
	}
}

func TestManagerApplyAcceptsExactEmptyNUMABucketBeforePluginWrites(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(
		commonstate.PoolNameReserve,
		commonstate.FakedContainerName,
		&cpustate.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
			AllocationResult: machine.NewCPUSet(0),
		},
	)
	state.SetAllocationInfo(
		commonstate.PoolNameReclaim,
		commonstate.FakedContainerName,
		&cpustate.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: machine.NewCPUSet(1),
		},
	)
	_, topology := testBulkheadStateAndTopology()
	plugin := &fakePlugin{name: "writer", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	got, err := m.Apply(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v, want exact empty NUMA bucket accepted", err)
	}
	if !got.IsEmpty() {
		t.Fatalf("Apply() reclaim = %s, want empty without topology owner", got.String())
	}
	if len(plugin.enableStates) != 1 || len(plugin.adjustViews) != 1 {
		t.Fatalf("valid exact NUMA projection did not reach plugin: enable=%d adjust=%d",
			len(plugin.enableStates), len(plugin.adjustViews))
	}
}

func TestManagerApplyPassesOwnedVerifiedViewToDependentsAndReturnsReclaim(t *testing.T) {
	t.Parallel()

	verified := machine.NewCPUSet(2, 3)
	override := &cpusetutil.CPUSetAdjustmentCommitOverride{}
	applied := &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
		NonReclaimPool:   machine.NewCPUSet(0),
		ReclaimEffective: verified,
		ReclaimEffectivePerNUMA: map[int]machine.CPUSet{
			0: machine.NewCPUSet(),
			1: verified,
		},
	}}
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          applied,
		},
	}
	dependent := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}

	in := enabledCPUSetAdjustmentCtx()
	in.CommitOverride = override
	got, err := m.Apply(context.Background(), in)
	if err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if !got.Equals(verified) {
		t.Fatalf("Apply() reclaim = %s, want %s", got.String(), verified.String())
	}
	if len(dependent.adjustOwnedViews) != 1 || dependent.adjustOwnedViews[0] == nil {
		t.Fatalf("dependent owned views = %#v, want one explicit view", dependent.adjustOwnedViews)
	}
	if !dependent.adjustOwnedViews[0].ReclaimEffective.Equals(verified) {
		t.Fatalf("dependent reclaim = %s, want %s",
			dependent.adjustOwnedViews[0].ReclaimEffective.String(), verified.String())
	}
	if !dependent.adjustOwnedViews[0].NonReclaimPool.Equals(machine.NewCPUSet(0)) {
		t.Fatalf("dependent non-reclaim = %s, want final-snapshot value 0",
			dependent.adjustOwnedViews[0].NonReclaimPool.String())
	}
	if !dependent.adjustOwnedViews[0].ReclaimEffectivePerNUMA[1].Equals(verified) {
		t.Fatalf("dependent reclaim NUMA bucket = %s, want final-snapshot value %s",
			dependent.adjustOwnedViews[0].ReclaimEffectivePerNUMA[1].String(), verified.String())
	}
	if !m.LatestAppliedReclaim().Equals(verified) {
		t.Fatalf("latest reclaim = %s, want %s", m.LatestAppliedReclaim().String(), verified.String())
	}
	if !override.ReclaimEffective.Equals(verified) || override.Source != "cpuset_topology" {
		t.Fatalf("fully-converged view commit override: reclaim=%s source=%q, want reclaim=%s source=%q",
			override.ReclaimEffective.String(), override.Source, verified.String(), "cpuset_topology")
	}
	applied.NonReclaimPool.Add(1)
	if dependent.adjustOwnedViews[0].NonReclaimPool.Contains(1) {
		t.Fatal("dependent view aliases topology result AppliedView")
	}
}

func TestManagerApplyRejectsFullyConvergedAppliedViewWithMissingPerNUMAProof(t *testing.T) {
	t.Parallel()

	topology, err := machine.GenerateDummyCPUTopology(64, 2, 2)
	if err != nil {
		t.Fatalf("GenerateDummyCPUTopology() error: %v", err)
	}
	state := cpustate.NewCPUPluginState(topology)
	state.SetAllocationInfo(
		commonstate.PoolNameReserve,
		commonstate.FakedContainerName,
		&cpustate.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
			AllocationResult: machine.NewCPUSet(),
		},
	)
	state.SetAllocationInfo(
		commonstate.PoolNameReclaim,
		commonstate.FakedContainerName,
		&cpustate.AllocationInfo{
			AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult: topology.CPUDetails.CPUs(),
		},
	)
	state.SetAllocationInfo("ramp-up-pod", "main", &cpustate.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			PodUid:        "ramp-up-pod",
			ContainerName: "main",
		},
		RampUp: true,
	})

	numa0 := topology.CPUDetails.CPUsInNUMANodes(0).ToSliceInt()
	numa1 := topology.CPUDetails.CPUsInNUMANodes(1).ToSliceInt()
	verified := machine.NewCPUSet(append(numa0[:6], numa1[:6]...)...)
	applied := model.NewDesiredView().ToAppliedView()
	applied.ReclaimEffective = verified
	applied.ReclaimEffectivePerNUMA = map[int]machine.CPUSet{
		0: machine.NewCPUSet(numa0[:6]...),
	}
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          applied,
		},
	}
	dependent := &fakePlugin{name: "workqueue", enabled: true}
	manager := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	override := &cpusetutil.CPUSetAdjustmentCommitOverride{}
	dynamicConf := dynamicBulkheadConf(true)
	dynamicConf.EnableReclaim = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition = true
	dynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.InitialRampUpReclaimCPUSetRatio = 0.2

	got, err := manager.Apply(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf:    dynamicConf,
		State:          state,
		Topology:       topology,
		CommitOverride: override,
	})
	if err == nil || !strings.Contains(err.Error(), "proof missing for NUMA 1") {
		t.Fatalf("Apply() error = %v, want missing per-NUMA applied proof", err)
	}
	if !got.IsEmpty() {
		t.Fatalf("Apply() reclaim = %s, want empty on hard-floor violation", got.String())
	}
	if len(dependent.adjustViews) != 0 {
		t.Fatalf("dependent calls = %d, want 0", len(dependent.adjustViews))
	}
	if manager.appliedView != nil || !manager.LatestAppliedReclaim().IsEmpty() {
		t.Fatalf("invalid applied view published: view=%+v reclaim=%s",
			manager.appliedView, manager.LatestAppliedReclaim().String())
	}
	if override.Source != "" || !override.ReclaimEffective.IsEmpty() {
		t.Fatalf("invalid applied view wrote commit override: %+v", override)
	}
}

func TestManagerPublishedPoolProjectionSurvivesSourceMutation(t *testing.T) {
	t.Parallel()

	identity := model.CPUSetPoolIdentity{
		Kind:         model.CPUSetPoolKindDedicated,
		PodNamespace: "default",
		PodName:      "dedicated-pod",
	}
	source := model.NewDesiredView().ToAppliedView()
	source.Level = model.AppliedViewLevelFull
	source.PoolProjection.CPUSetByIdentity[identity] = machine.NewCPUSet(1)
	source.PoolProjection.UncoveredCPUs = machine.NewCPUSet(2)
	source.PoolProjection.AmbiguousCPUs = machine.NewCPUSet(3)

	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          source,
		},
	}
	periodicalConsumer := &fakePlugin{name: "periodical-consumer", enabled: true}
	manager := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, periodicalConsumer}}

	if _, err := manager.Apply(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}

	source.PoolProjection.CPUSetByIdentity[identity].Add(99)
	source.PoolProjection.UncoveredCPUs.Add(98)
	source.PoolProjection.AmbiguousCPUs.Add(97)

	manager.RunPeriodicalHandlers(nil, nil, enabledDynamicAgentConf(), nil, nil)
	if len(periodicalConsumer.periodicApplied) != 1 || periodicalConsumer.periodicApplied[0] == nil {
		t.Fatalf("periodical applied views = %#v, want one published view", periodicalConsumer.periodicApplied)
	}

	published := periodicalConsumer.periodicApplied[0].PoolProjection
	assertCPUSet(t, "published dedicated pool", published.CPUSetByIdentity[identity], "1")
	assertCPUSet(t, "published uncovered CPUs", published.UncoveredCPUs, "2")
	assertCPUSet(t, "published ambiguous CPUs", published.AmbiguousCPUs, "3")
}

func TestManagerApplyPublishesPartitionMetricsAfterAppliedViewCommit(t *testing.T) {
	t.Parallel()

	state, topology := testBulkheadStateAndTopology()
	applied := model.NewDesiredView().ToAppliedView()
	applied.Reserve = machine.NewCPUSet(0)
	applied.ReclaimEffective = machine.NewCPUSet(1, 2, 3)
	applied.NonReclaimPool = machine.NewCPUSet(0)
	applied.SharePoolMap[commonstate.PoolNameShare] = machine.NewCPUSet()
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          applied,
		},
	}
	emitter := &capturingEmitter{}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()
	in.DynamicConf.FillDefaultSharePoolWithNonReclaimCPUs = true
	in.State = state
	in.Topology = topology
	in.Emitter = emitter

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	for _, viewName := range []string{"desired", "applied"} {
		if !hasMetricTags(emitter.records, metricBulkheadPartitionCPUCores, "view", viewName) {
			t.Fatalf("partition metrics missing view=%s: %#v", viewName, emitter.records)
		}
		if !hasMetricTags(emitter.records, metricBulkheadDefaultShareResidualCPUCores, "view", viewName) {
			t.Fatalf("default-share residual metric missing view=%s: %#v", viewName, emitter.records)
		}
	}
	if !hasMetric(emitter.records, metricBulkheadPartitionCPUDiffCores) {
		t.Fatalf("partition diff metrics missing after committed applied view: %#v", emitter.records)
	}
}

func TestManagerApplyPartitionMetricsUsePostTopologyDesiredView(t *testing.T) {
	t.Parallel()

	state, topology := testBulkheadStateAndTopology()
	applied := model.NewDesiredView().ToAppliedView()
	applied.Reserve = machine.NewCPUSet(0)
	applied.ReclaimEffective = machine.NewCPUSet(1, 2)
	applied.NonReclaimPool = machine.NewCPUSet(3)
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          applied,
		},
		mutateDesired: func(desired *model.DesiredView) {
			desired.ReclaimEffective = machine.NewCPUSet(1, 2)
			desired.NonReclaimPool = machine.NewCPUSet(3)
		},
	}
	emitter := &capturingEmitter{}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()
	in.State = state
	in.Topology = topology
	in.Emitter = emitter

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	want := map[string]float64{
		metricKey(metricBulkheadPartitionCPUCores, "view", "desired", "partition", "reclaim"):               2,
		metricKey(metricBulkheadPartitionCPUDiffCores, "partition", "reclaim", "direction", "desired_only"): 0,
		metricKey(metricBulkheadPartitionCPUDiffCores, "partition", "reclaim", "direction", "applied_only"): 0,
	}
	assertMetricValues(t, emitter.records, want)
}

func TestManagerApplyDoesNotPublishResidualMetricWhenBackfillDisabled(t *testing.T) {
	t.Parallel()

	state, topology := testBulkheadStateAndTopology()
	emitter := &capturingEmitter{}
	m := &Manager{plugins: []bulkheadapi.Plugin{
		&fakePlugin{name: "writer", enabled: true},
	}}
	in := enabledCPUSetAdjustmentCtx()
	in.State = state
	in.Topology = topology
	in.Emitter = emitter

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("Apply() error: %v", err)
	}
	if hasMetric(emitter.records, metricBulkheadDefaultShareResidualCPUCores) {
		t.Fatalf("disabled backfill published residual metrics: %#v", emitter.records)
	}
	if !hasMetricTags(emitter.records, metricBulkheadPartitionCPUCores, "view", "desired", "partition", "default_share") {
		t.Fatalf("disabled backfill must retain generic default-share partition metric: %#v", emitter.records)
	}
}

func TestManagerApplyDoesNotPublishAppliedPartitionMetricsWhenTopologyDoesNotConverge(t *testing.T) {
	t.Parallel()

	state, topology := testBulkheadStateAndTopology()
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FinalSnapshotCurrent: true,
			AppliedView:          model.NewDesiredView().ToAppliedView(),
		},
	}
	emitter := &capturingEmitter{}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	in := enabledCPUSetAdjustmentCtx()
	in.State = state
	in.Topology = topology
	in.Emitter = emitter

	if _, err := m.Apply(context.Background(), in); err == nil {
		t.Fatal("Apply() error = nil, want non-converged result rejected")
	}
	if !hasMetricTags(emitter.records, metricBulkheadPartitionCPUCores, "view", "desired") {
		t.Fatalf("desired partition metrics missing: %#v", emitter.records)
	}
	if hasMetricTags(emitter.records, metricBulkheadPartitionCPUCores, "view", "applied") {
		t.Fatalf("non-converged result published applied partition metrics: %#v", emitter.records)
	}
	if hasMetric(emitter.records, metricBulkheadPartitionCPUDiffCores) {
		t.Fatalf("non-converged result published diff metrics: %#v", emitter.records)
	}
}

func TestManagerLatestAppliedReclaimPublishesAndReturnsClones(t *testing.T) {
	t.Parallel()

	m := &Manager{}
	source := machine.NewCPUSet(1, 2)
	m.publishLatestAppliedReclaim(source)
	source.Add(3)

	first := m.LatestAppliedReclaim()
	if !first.Equals(machine.NewCPUSet(1, 2)) {
		t.Fatalf("first latest reclaim = %s, want 1-2", first.String())
	}
	first.Add(4)
	second := m.LatestAppliedReclaim()
	if !second.Equals(machine.NewCPUSet(1, 2)) {
		t.Fatalf("second latest reclaim = %s, want 1-2", second.String())
	}
}

func TestManagerApplyDoesNotPublishTypedTopologyResultBeforeDependentsSucceed(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView: &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
				ReclaimEffective: machine.NewCPUSet(2, 3),
			}},
		},
	}
	dependent := &fakePlugin{name: "workqueue", enabled: true, adjustErr: errors.New("dependent failed")}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	state, topology := testBulkheadStateAndTopology()
	in := enabledCPUSetAdjustmentCtx()
	in.State = state
	in.Topology = topology

	if _, err := m.Apply(context.Background(), in); err == nil {
		t.Fatal("Apply() error = nil, want dependent failure")
	}
	if m.appliedView != nil {
		t.Fatalf("failed transaction published applied view: %#v", m.appliedView)
	}
	if m.appliedViewRevision != 0 {
		t.Fatalf("failed transaction revision = %d, want 0", m.appliedViewRevision)
	}
	if !m.LatestAppliedReclaim().IsEmpty() {
		t.Fatalf("failed transaction published reclaim %s", m.LatestAppliedReclaim().String())
	}
}

func TestManagerApplyRejectsStaleGenerationBeforeDependentSideEffects(t *testing.T) {
	t.Parallel()

	oldApplied := &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
		ReclaimEffective: machine.NewCPUSet(3),
	}}
	newApplied := &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
		ReclaimEffective: machine.NewCPUSet(1, 2),
	}}
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          newApplied,
		},
	}
	dependent := &fakePlugin{name: "rdt_cpulist", enabled: true}
	m := &Manager{
		plugins:                       []bulkheadapi.Plugin{topologyPlugin, dependent},
		appliedView:                   oldApplied,
		appliedViewRevision:           7,
		appliedViewValidForPeriodical: true,
	}
	m.publishLatestAppliedReclaim(oldApplied.ReclaimEffective)
	state, topology := testBulkheadStateAndTopology()
	fenceCalls := 0
	emitter := &capturingEmitter{}

	_, err := m.Apply(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
		Emitter:     emitter,
		Generation:  11,
		CommitIfGenerationCurrent: func(generation uint64, commit func()) bool {
			if generation != 11 {
				t.Fatalf("commit generation = %d, want 11", generation)
			}
			fenceCalls++
			if fenceCalls <= 2 {
				commit()
				return true
			}
			return false
		},
	})
	var nonConverged *NonConvergedError
	if !errors.As(err, &nonConverged) {
		t.Fatalf("Apply() error = %v, want stale generation rejected as *NonConvergedError", err)
	}
	if got := len(dependent.adjustViews); got != 0 {
		t.Fatalf("dependent calls = %d, want stale generation rejected before dependent side effects", got)
	}
	if m.appliedViewRevision != 7 {
		t.Fatalf("applied revision = %d, want old revision 7 retained", m.appliedViewRevision)
	}
	assertCPUSet(t, "retained applied reclaim", m.appliedView.ReclaimEffective, "3")
	assertCPUSet(t, "retained latest reclaim", m.LatestAppliedReclaim(), "3")
	if m.appliedViewValidForPeriodical {
		t.Fatal("stale generation must not authorize periodical handlers")
	}
	if hasMetricTags(emitter.records, metricBulkheadPartitionCPUCores, "view", "applied") {
		t.Fatalf("stale generation published applied partition metrics: %#v", emitter.records)
	}
	if hasMetric(emitter.records, metricBulkheadPartitionCPUDiffCores) {
		t.Fatalf("stale generation published partition diff metrics: %#v", emitter.records)
	}
}

func TestManagerApplyRejectsStaleGenerationBeforeSideEffects(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "rdt_cpulist", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	_, err := m.Apply(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		Generation:  10,
		CommitIfGenerationCurrent: func(uint64, func()) bool {
			return false
		},
	})
	var nonConverged *NonConvergedError
	if !errors.As(err, &nonConverged) {
		t.Fatalf("Apply() error = %v, want stale generation rejected as *NonConvergedError", err)
	}
	if got := len(plugin.adjustViews); got != 0 {
		t.Fatalf("plugin calls = %d, want stale round rejected before side effects", got)
	}
}

func TestManagerApplyChecksGenerationAfterFailedSideEffect(t *testing.T) {
	t.Parallel()

	pluginErr := errors.New("partial side effect failed")
	plugin := &fakePlugin{name: "rdt_cpulist", enabled: true, adjustErr: pluginErr}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}
	fenceCalls := 0

	_, err := m.Apply(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		Generation:  10,
		CommitIfGenerationCurrent: func(uint64, func()) bool {
			fenceCalls++
			return fenceCalls <= 2
		},
	})

	var nonConverged *NonConvergedError
	if !errors.As(err, &nonConverged) {
		t.Fatalf("Apply() error = %v, want stale generation after failed side effect", err)
	}
	if fenceCalls != 3 {
		t.Fatalf("generation fence calls = %d, want entry, pre-, and post-side-effect checks", fenceCalls)
	}
	if got := len(plugin.adjustViews); got != 1 {
		t.Fatalf("plugin calls = %d, want failed side effect to run once", got)
	}
}

func TestManagerApplyChecksGenerationAfterDisabledHandlerError(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{
		name:        "rdt_cpulist",
		enabled:     false,
		disabledErr: errors.New("partial disabled reset failed"),
	}
	m := &Manager{
		plugins:                     []bulkheadapi.Plugin{plugin},
		lastCPUSetAdjustmentEnabled: map[string]bool{plugin.name: true},
	}
	fenceCalls := 0

	_, err := m.Apply(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		Generation:  10,
		CommitIfGenerationCurrent: func(uint64, func()) bool {
			fenceCalls++
			return fenceCalls <= 2
		},
	})

	var nonConverged *NonConvergedError
	if !errors.As(err, &nonConverged) {
		t.Fatalf("Apply() error = %v, want stale generation after failed disabled reset", err)
	}
	if fenceCalls != 3 {
		t.Fatalf("generation fence calls = %d, want entry, pre-, and post-disabled checks", fenceCalls)
	}
	if plugin.disabledCalls != 1 {
		t.Fatalf("disabled calls = %d, want failed reset to run once", plugin.disabledCalls)
	}
}

func TestManagerApplyChecksGenerationAfterTopologyError(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		err:        errors.New("partial topology apply failed"),
	}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin}}
	fenceCalls := 0

	_, err := m.Apply(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		Generation:  10,
		CommitIfGenerationCurrent: func(uint64, func()) bool {
			fenceCalls++
			return fenceCalls <= 2
		},
	})

	var nonConverged *NonConvergedError
	if !errors.As(err, &nonConverged) {
		t.Fatalf("Apply() error = %v, want stale generation after failed topology apply", err)
	}
	if fenceCalls != 3 {
		t.Fatalf("generation fence calls = %d, want entry, pre-, and post-topology checks", fenceCalls)
	}
}

func TestManagerApplyRejectsTypedTopologyResultWhenDesiredViewChanges(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(1, 2, 3),
	})
	_, topology := testBulkheadStateAndTopology()
	oldApplied := &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
		ReclaimEffective: machine.NewCPUSet(3),
	}}
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView: &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
				ReclaimEffective: machine.NewCPUSet(1),
			}},
		},
		afterApply: func() {
			state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(2, 3),
			})
		},
	}
	dependent := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{
		plugins:             []bulkheadapi.Plugin{topologyPlugin, dependent},
		appliedView:         oldApplied,
		appliedViewRevision: 7,
	}

	_, err := m.Apply(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	})
	var nonConverged *NonConvergedError
	if !errors.As(err, &nonConverged) {
		t.Fatalf("Apply() error = %v, want stale typed result rejected as *NonConvergedError", err)
	}
	if got := len(dependent.adjustViews); got != 0 {
		t.Fatalf("dependent calls = %d, want 0 after desired view changes", got)
	}
	if m.appliedViewRevision != 7 {
		t.Fatalf("applied revision = %d, want old revision 7 retained", m.appliedViewRevision)
	}
	assertCPUSet(t, "retained applied reclaim", m.appliedView.ReclaimEffective, "3")
}

func TestManagerApplyRetainsRevisionForConsecutiveIdenticalTypedAppliedViews(t *testing.T) {
	t.Parallel()

	state, topology := testBulkheadStateAndTopology()
	applied := &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
		NonReclaimPool:   machine.NewCPUSet(0),
		ReclaimEffective: machine.NewCPUSet(1, 2, 3),
	}}
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          applied,
		},
	}
	dependent := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	in := cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	}

	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("first Apply() error: %v", err)
	}
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("second Apply() error: %v", err)
	}
	if m.appliedViewRevision != 1 {
		t.Fatalf("applied revision = %d, want 1 for identical payloads", m.appliedViewRevision)
	}
	if !reflect.DeepEqual(dependent.adjustRevision, []uint64{1, 1}) {
		t.Fatalf("dependent revisions = %v, want [1 1]", dependent.adjustRevision)
	}
}

func TestRunCPUSetAdjustmentHandlersCallsEnabledPluginEveryRun(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if got := len(plugin.adjustViews); got != 2 {
		t.Fatalf("manager should not skip enabled plugin, got %d calls", got)
	}
}

func TestRunCPUSetAdjustmentHandlersReconcilesWhenNonReclaimPoolMinSizeChanges(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}
	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	// SysAdvisor emits a reclaim affinity budget (ReclaimRaw). QRM bounds the
	// effective reclaim set by this budget and derives non-reclaim from the
	// remainder, so the min-size padding pulls CPUs back out of advisor reclaim.
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(1, 2, 3),
	})
	topology := &machine.CPUTopology{
		NumCPUs:      4,
		NumCores:     4,
		NumSockets:   1,
		NumNUMANodes: 1,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
		},
	}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConfWithMinSize(true, 0),
		State:       state,
		Topology:    topology,
	}); err != nil {
		t.Fatalf("first run failed: %v", err)
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConfWithMinSize(true, 2),
		State:       state,
		Topology:    topology,
	}); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if got := len(plugin.adjustViews); got != 2 {
		t.Fatalf("min size change should trigger plugin, got %d calls", got)
	}
	assertCPUSet(t, "first non reclaim", plugin.adjustViews[0].NonReclaimPool, "")
	assertCPUSet(t, "second non reclaim", plugin.adjustViews[1].NonReclaimPool, "1-2")
}

func TestRunCPUSetAdjustmentHandlersUsesDefaultNonReclaimPoolMinSize(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{
		plugins:                      []bulkheadapi.Plugin{plugin},
		defaultNonReclaimPoolMinSize: 2,
	}
	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	// Advisor reclaim budget: effective reclaim is bounded by this set and the
	// default min-size pads non-reclaim back out of it.
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(1, 2, 3),
	})
	topology := &machine.CPUTopology{
		NumCPUs:      4,
		NumCores:     4,
		NumSockets:   1,
		NumNUMANodes: 1,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 0, SocketID: 0, CoreID: 2},
			3: {NUMANodeID: 0, SocketID: 0, CoreID: 3},
		},
	}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConfWithMinSize(true, 0),
		State:       state,
		Topology:    topology,
	}); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if got := len(plugin.adjustViews); got != 1 {
		t.Fatalf("adjust calls = %d, want 1", got)
	}
	assertCPUSet(t, "non reclaim", plugin.adjustViews[0].NonReclaimPool, "1-2")
	assertCPUSet(t, "reclaim effective", plugin.adjustViews[0].ReclaimEffective, "3")
}

func TestRunCPUSetAdjustmentHandlersPassesHandlerContextToEnable(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}
	in := cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       cpustate.NewCPUPluginState(nil),
	}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), in); err != nil {
		t.Fatalf("run failed: %v", err)
	}
	if len(plugin.enableStates) != 1 {
		t.Fatalf("Enable calls = %d, want 1", len(plugin.enableStates))
	}
	if plugin.enableStates[0] != in.State {
		t.Fatalf("Enable did not receive handler context state")
	}
}

func TestRunCPUSetAdjustmentHandlersSkipsAllPluginLogicWhenBulkheadDisabled(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{
		plugins:                     []bulkheadapi.Plugin{plugin},
		lastCPUSetAdjustmentEnabled: map[string]bool{plugin.Name(): true},
	}

	err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(false),
		State:       cpustate.NewCPUPluginState(nil),
	})
	if err != nil {
		t.Fatalf("RunCPUSetAdjustmentHandlers failed: %v", err)
	}
	if len(plugin.enableStates) != 0 {
		t.Fatalf("plugin Enable calls = %d, want 0", len(plugin.enableStates))
	}
	if len(plugin.adjustViews) != 0 {
		t.Fatalf("adjust calls = %d, want 0", len(plugin.adjustViews))
	}
	if plugin.disabledCalls != 0 {
		t.Fatalf("disabled calls = %d, want 0", plugin.disabledCalls)
	}
	if m.lastCPUSetAdjustmentEnabled != nil {
		t.Fatalf("last enabled state should be cleared when bulkhead is globally disabled")
	}
}

func TestRunCPUSetAdjustmentHandlersReconcilesAfterBulkheadReenabledWithSameView(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("first enabled run failed: %v", err)
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(false),
	}); err != nil {
		t.Fatalf("disabled bulkhead run failed: %v", err)
	}
	if plugin.disabledCalls != 0 {
		t.Fatalf("disabled calls = %d, want 0", plugin.disabledCalls)
	}
	if len(plugin.enableStates) != 1 {
		t.Fatalf("plugin Enable calls = %d, want 1", len(plugin.enableStates))
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("second enabled run failed: %v", err)
	}
	if got := len(plugin.adjustViews); got != 2 {
		t.Fatalf("adjust calls = %d, want 2", got)
	}
}

func TestRunCPUSetAdjustmentHandlersCallsDisabledTransitionWhenPluginDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		pluginEnabled      bool
		preRunEnabledState bool
		wantAdjustCalls    int
		wantDisabledCalls  int
	}{
		{
			name:            "plugin enabled",
			pluginEnabled:   true,
			wantAdjustCalls: 1,
		},
		{
			name:              "plugin disabled without previous enabled state",
			wantDisabledCalls: 1,
		},
		{
			name:               "bulkhead disabled after previous enabled",
			preRunEnabledState: true,
			wantDisabledCalls:  1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plugin := &fakePlugin{name: "fake", enabled: tt.pluginEnabled}
			m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}
			if tt.preRunEnabledState {
				m.lastCPUSetAdjustmentEnabled = map[string]bool{plugin.Name(): true}
			}

			err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
				DynamicConf: dynamicBulkheadConf(true),
			})
			if err != nil {
				t.Fatalf("RunCPUSetAdjustmentHandlers failed: %v", err)
			}
			if got := len(plugin.adjustViews); got != tt.wantAdjustCalls {
				t.Fatalf("adjust calls = %d, want %d", got, tt.wantAdjustCalls)
			}
			if plugin.disabledCalls != tt.wantDisabledCalls {
				t.Fatalf("disabled calls = %d, want %d", plugin.disabledCalls, tt.wantDisabledCalls)
			}
		})
	}
}

func TestRunCPUSetAdjustmentHandlersResetsDisabledPluginWhenLastEnabledNil(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: false}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("first disabled run failed: %v", err)
	}
	if plugin.disabledCalls != 1 {
		t.Fatalf("disabled calls = %d, want 1", plugin.disabledCalls)
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("stable disabled run failed: %v", err)
	}
	if plugin.disabledCalls != 1 {
		t.Fatalf("stable disabled calls = %d, want 1", plugin.disabledCalls)
	}
	if len(plugin.adjustViews) != 0 {
		t.Fatalf("adjust calls = %d, want 0", len(plugin.adjustViews))
	}
	if got := m.lastCPUSetAdjustmentEnabled[plugin.Name()]; got {
		t.Fatalf("last enabled state = %t, want false", got)
	}
}

func TestRunCPUSetAdjustmentHandlersDoesNotCacheFailedView(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true, adjustErr: errors.New("boom")}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err == nil {
		t.Fatal("expected first run to fail")
	}
	plugin.adjustErr = nil
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("second run failed: %v", err)
	}
	if got := len(plugin.adjustViews); got != 2 {
		t.Fatalf("expected failed view not cached, got %d calls", got)
	}
}

func TestRunCPUSetAdjustmentHandlersPublishesAppliedViewAfterTopologyConverges_BitsUT(t *testing.T) {
	t.Parallel()

	topologyApplied := &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
		NonReclaimPool:   machine.NewCPUSet(0, 2, 3),
		ReclaimEffective: machine.NewCPUSet(1),
	}}
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          topologyApplied,
		},
	}
	consumer := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, consumer}}
	state, topology := testBulkheadStateAndTopology()

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	}); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if got := len(consumer.adjustApplied); got != 1 {
		t.Fatalf("consumer applied view calls = %d, want 1", got)
	}
	if consumer.adjustApplied[0] == nil {
		t.Fatalf("consumer should receive published AppliedView")
	}
	if consumer.adjustRevision[0] != 1 {
		t.Fatalf("consumer applied revision = %d, want 1", consumer.adjustRevision[0])
	}
	if len(topologyPlugin.sawApplied) != 1 || topologyPlugin.sawApplied[0] != nil {
		t.Fatalf("topology plugin should see previous (nil) applied view before publish, got %v", topologyPlugin.sawApplied)
	}
	assertCPUSet(t, "consumer applied reclaim from final snapshot", consumer.adjustApplied[0].ReclaimEffective, "1")

	consumer.adjustApplied[0].ReclaimEffective.Add(99)
	if m.appliedView.ReclaimEffective.Contains(99) {
		t.Fatalf("mutating consumer AppliedView copy should not mutate manager published view")
	}
}

func TestRunCPUSetAdjustmentHandlersABCLifecyclePreservesBOnPlanningFailure(t *testing.T) {
	t.Parallel()

	reclaimB := machine.MustParse("9-13,20-21,36,39-40,42,57,59-60,68,84,87-88,90")
	reclaimC := machine.MustParse("13-18,20-21,35-40,42,62-66,68,83-88,90")
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView: &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
				Reserve:          machine.NewCPUSet(0, 24),
				ReclaimEffective: reclaimB,
			}},
		},
	}
	consumer := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, consumer}}
	state, topology := testBulkheadStateAndTopology()
	run := func() error {
		return m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
			DynamicConf: dynamicBulkheadConf(true),
			State:       state,
			Topology:    topology,
		})
	}

	if err := run(); err != nil {
		t.Fatalf("publish B: %v", err)
	}
	assertCPUSet(t, "applied B", m.appliedView.ReclaimEffective, reclaimB.String())
	if m.appliedViewRevision != 1 {
		t.Fatalf("B revision = %d, want 1", m.appliedViewRevision)
	}

	topologyPlugin.err = errors.New("plan C: deadlock probe budget exceeded")
	if err := run(); err == nil {
		t.Fatal("planning C should fail")
	}
	assertCPUSet(t, "applied remains B", m.appliedView.ReclaimEffective, reclaimB.String())
	if m.appliedViewRevision != 1 {
		t.Fatalf("failed C revision = %d, want 1", m.appliedViewRevision)
	}
	if got := len(consumer.adjustApplied); got != 1 {
		t.Fatalf("consumer calls after failed C = %d, want 1", got)
	}

	topologyPlugin.err = nil
	topologyPlugin.result = fakeDAGResult{
		FullyConverged:       true,
		FinalSnapshotCurrent: true,
		AppliedView: &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
			Reserve:          machine.NewCPUSet(0, 24),
			ReclaimEffective: reclaimC,
		}},
	}
	if err := run(); err != nil {
		t.Fatalf("publish C: %v", err)
	}
	assertCPUSet(t, "applied C", m.appliedView.ReclaimEffective, reclaimC.String())
	assertCPUSet(t, "reserve C", m.appliedView.Reserve, "0,24")
	if m.appliedViewRevision != 2 {
		t.Fatalf("C revision = %d, want 2", m.appliedViewRevision)
	}
	if got := len(consumer.adjustApplied); got != 2 {
		t.Fatalf("consumer calls after C = %d, want 2", got)
	}
	assertCPUSet(t, "consumer sees C", consumer.adjustApplied[1].ReclaimEffective, reclaimC.String())
}

func TestRunCPUSetAdjustmentHandlersShortCircuitsConsumersUntilTopologyConverges_BitsUT(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakePlugin{name: "cpuset_topology", enabled: true}
	consumer := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, consumer}}
	// Legacy callback-path owner: it is not a typed TopologyPlugin, so pin the
	// owner identity the way discovery would for the production plugin.
	m.topologyOwner, m.topologyDiscovered = topologyPlugin, true
	state, topology := testBulkheadStateAndTopology()

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	}); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if got := len(consumer.adjustViews); got != 0 {
		t.Fatalf("consumer calls = %d, want 0 before topology convergence", got)
	}
	if m.appliedView != nil || m.appliedViewRevision != 0 {
		t.Fatalf("manager should not publish applied view before convergence, view=%v revision=%d", m.appliedView, m.appliedViewRevision)
	}
}

func TestRunCPUSetAdjustmentHandlersRetainsRevisionWhenTopologyResultIsNotCurrent_BitsUT(t *testing.T) {
	t.Parallel()

	oldApplied := &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
		ReclaimEffective: machine.NewCPUSet(3),
	}}
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: false,
			AppliedView: &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
				ReclaimEffective: machine.NewCPUSet(1),
			}},
		},
	}
	consumer := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{
		plugins:             []bulkheadapi.Plugin{topologyPlugin, consumer},
		appliedView:         oldApplied,
		appliedViewRevision: 7,
	}
	state, topology := testBulkheadStateAndTopology()

	err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	})
	var nonConv *NonConvergedError
	if !errors.As(err, &nonConv) {
		t.Fatalf("err=%v, want NonConvergedError for stale final snapshot", err)
	}

	if got := len(consumer.adjustViews); got != 0 {
		t.Fatalf("consumer calls = %d, want 0 for stale final snapshot", got)
	}
	if m.appliedViewRevision != 7 {
		t.Fatalf("applied revision = %d, want old revision 7 retained", m.appliedViewRevision)
	}
	assertCPUSet(t, "retained applied reclaim", m.appliedView.ReclaimEffective, "3")
}

func TestRunCPUSetAdjustmentHandlersRetainsRevisionWhenDesiredChangesBeforeTopologyCallback_BitsUT(t *testing.T) {
	t.Parallel()

	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(1, 2, 3),
	})
	_, topology := testBulkheadStateAndTopology()
	oldApplied := &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
		ReclaimEffective: machine.NewCPUSet(3),
	}}
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView: &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
				ReclaimEffective: machine.NewCPUSet(1),
			}},
		},
		afterApply: func() {
			state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
				AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
				AllocationResult: machine.NewCPUSet(2, 3),
			})
		},
	}
	consumer := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{
		plugins:             []bulkheadapi.Plugin{topologyPlugin, consumer},
		appliedView:         oldApplied,
		appliedViewRevision: 7,
	}

	err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	})
	var nonConv *NonConvergedError
	if !errors.As(err, &nonConv) {
		t.Fatalf("err=%v, want NonConvergedError when desired intent drifts after apply", err)
	}

	if got := len(consumer.adjustViews); got != 0 {
		t.Fatalf("consumer calls = %d, want 0 after desired intent changes", got)
	}
	if m.appliedViewRevision != 7 {
		t.Fatalf("applied revision = %d, want old revision 7 retained", m.appliedViewRevision)
	}
	assertCPUSet(t, "retained applied reclaim", m.appliedView.ReclaimEffective, "3")
}

func TestRunCPUSetAdjustmentHandlersShortCircuitsConsumersWhenTopologyDisabled_BitsUT(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakePlugin{name: "cpuset_topology", enabled: false}
	consumer := &fakePlugin{name: "workqueue", enabled: true}
	m := &Manager{
		plugins:                     []bulkheadapi.Plugin{topologyPlugin, consumer},
		appliedView:                 (&model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{ReclaimEffective: machine.NewCPUSet(1)}}).ToAppliedView(),
		appliedViewRevision:         1,
		lastCPUSetAdjustmentEnabled: map[string]bool{topologyPlugin.Name(): true, consumer.Name(): true},
	}
	m.topologyOwner, m.topologyDiscovered = topologyPlugin, true
	state, topology := testBulkheadStateAndTopology()

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	}); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if got := len(consumer.adjustViews); got != 0 {
		t.Fatalf("consumer calls = %d, want 0 when topology is disabled", got)
	}
	if m.appliedViewRevision != 1 {
		t.Fatalf("applied revision = %d, want old revision 1 retained", m.appliedViewRevision)
	}
}

func TestRunCPUSetAdjustmentHandlersRunsDisabledResetAfterTopologyDisabled_BitsUT(t *testing.T) {
	t.Parallel()

	topologyPlugin := &fakePlugin{name: "cpuset_topology", enabled: false}
	enabledConsumer := &fakePlugin{name: "cpuset_mems", enabled: true}
	disabledReset := &fakePlugin{name: "workqueue", enabled: false}
	m := &Manager{
		plugins: []bulkheadapi.Plugin{topologyPlugin, enabledConsumer, disabledReset},
		lastCPUSetAdjustmentEnabled: map[string]bool{
			topologyPlugin.Name():  true,
			enabledConsumer.Name(): true,
			disabledReset.Name():   true,
		},
	}
	m.topologyOwner, m.topologyDiscovered = topologyPlugin, true
	state, topology := testBulkheadStateAndTopology()

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	}); err != nil {
		t.Fatalf("run failed: %v", err)
	}

	if got := len(enabledConsumer.adjustViews); got != 0 {
		t.Fatalf("enabled consumer calls = %d, want 0 when topology is disabled", got)
	}
	if topologyPlugin.disabledCalls != 1 {
		t.Fatalf("topology disabled calls = %d, want 1", topologyPlugin.disabledCalls)
	}
	if disabledReset.disabledCalls != 1 {
		t.Fatalf("downstream disabled reset calls = %d, want 1", disabledReset.disabledCalls)
	}
	if m.appliedViewValidForPeriodical {
		t.Fatalf("periodical AppliedView must remain invalid when topology is disabled")
	}
}

func TestRunPeriodicalHandlersWithholdsOldAppliedViewWhenTopologyNotPublished_BitsUT(t *testing.T) {
	t.Parallel()

	convergedView := &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
		ReclaimEffective: machine.NewCPUSet(1, 2, 3),
	}}
	topologyPlugin := &fakeTopologyPlugin{
		fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true},
		result: fakeDAGResult{
			FullyConverged:       true,
			FinalSnapshotCurrent: true,
			AppliedView:          convergedView,
		},
	}
	systemService := &fakePlugin{name: "system_service", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, systemService}}
	state, topology := testBulkheadStateAndTopology()

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	}); err != nil {
		t.Fatalf("initial converged run failed: %v", err)
	}
	if m.appliedView == nil || m.appliedViewRevision != 1 {
		t.Fatalf("initial run should publish internal applied view, view=%v revision=%d", m.appliedView, m.appliedViewRevision)
	}

	topologyPlugin.result = fakeDAGResult{}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
		State:       state,
		Topology:    topology,
	}); err == nil {
		t.Fatal("non-converged run err=nil, want NonConvergedError")
	}
	if m.appliedView == nil || m.appliedViewRevision != 1 {
		t.Fatalf("non-publish run must retain internal old applied view/revision, view=%v revision=%d", m.appliedView, m.appliedViewRevision)
	}

	m.RunPeriodicalHandlers(nil, nil, enabledDynamicAgentConf(), nil, nil)
	if systemService.periodicCalls != 1 {
		t.Fatalf("periodical calls = %d, want 1", systemService.periodicCalls)
	}
	if systemService.periodicApplied[0] != nil {
		t.Fatalf("periodical context should withhold old AppliedView when current round did not publish")
	}
	if systemService.periodicRevision[0] != 0 {
		t.Fatalf("periodical revision = %d, want 0 for unpublished current round", systemService.periodicRevision[0])
	}
	if systemService.periodicValid[0] {
		t.Fatalf("periodical valid flag = true, want false for unpublished current round")
	}
}

func TestRunCPUSetAdjustmentHandlersDisabledTransitionInvalidatesCache(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("first enabled run failed: %v", err)
	}

	plugin.enabled = false
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("disabled transition failed: %v", err)
	}

	plugin.enabled = true
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("second enabled run failed: %v", err)
	}

	if got := len(plugin.adjustViews); got != 2 {
		t.Fatalf("expected second enabled run not to be skipped after disabled transition, got %d calls", got)
	}
}

func TestRunCPUSetAdjustmentHandlersCallsDisabledTransitionOnceForPluginDisable(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("enabled run failed: %v", err)
	}

	plugin.enabled = false
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("disabled transition failed: %v", err)
	}
	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("stable disabled run failed: %v", err)
	}

	if plugin.disabledCalls != 1 {
		t.Fatalf("disabled transition calls = %d, want 1", plugin.disabledCalls)
	}
}

func TestRunCPUSetAdjustmentHandlersReturnsDisabledHandlerError(t *testing.T) {
	t.Parallel()

	plugin := &fakePlugin{name: "fake", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}

	if err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx()); err != nil {
		t.Fatalf("enabled run failed: %v", err)
	}

	plugin.enabled = false
	plugin.disabledErr = errors.New("disabled failed")
	err := m.RunCPUSetAdjustmentHandlers(context.Background(), enabledCPUSetAdjustmentCtx())
	if err == nil {
		t.Fatalf("expected disabled transition error")
	}
	if got := err.Error(); !strings.Contains(got, "disabled transition failed") {
		t.Fatalf("error = %q, want disabled transition failed", got)
	}
}

func TestRunPeriodicalHandlersContinuesAfterErrors(t *testing.T) {
	t.Parallel()

	pluginA := &fakePlugin{name: "a", periodicErr: errors.New("a failed")}
	pluginB := &fakePlugin{name: "b"}
	m := &Manager{plugins: []bulkheadapi.Plugin{pluginA, pluginB}}

	m.RunPeriodicalHandlers(nil, nil, enabledDynamicAgentConf(), nil, nil)
	if pluginA.periodicCalls != 1 || pluginB.periodicCalls != 1 {
		t.Fatalf("expected both plugins to run, got a=%d b=%d", pluginA.periodicCalls, pluginB.periodicCalls)
	}
}

func TestRunPeriodicalHandlersSkipsAllPluginLogicWhenBulkheadDisabled(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		bulkheadEnabled bool
		wantCalls       int
	}{
		{
			name: "bulkhead disabled",
		},
		{
			name:            "bulkhead enabled",
			bulkheadEnabled: true,
			wantCalls:       1,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			plugin := &fakePlugin{name: "fake"}
			m := &Manager{plugins: []bulkheadapi.Plugin{plugin}}
			dynamicConf := dynamicconfig.NewDynamicAgentConfiguration()
			dynamicConf.SetDynamicConfiguration(dynamicBulkheadConf(tt.bulkheadEnabled))

			m.RunPeriodicalHandlers(nil, nil, dynamicConf, nil, nil)
			if plugin.periodicCalls != tt.wantCalls {
				t.Fatalf("periodic calls = %d, want %d", plugin.periodicCalls, tt.wantCalls)
			}
			if len(plugin.periodicStates) != tt.wantCalls {
				t.Fatalf("periodic states = %d, want %d", len(plugin.periodicStates), tt.wantCalls)
			}
		})
	}
}

func TestEmitBulkheadPluginResultFormatsReasonTag(t *testing.T) {
	t.Parallel()

	emitter := &capturingEmitter{}
	rawReason := "apply cpuset failed at kubepods/podabc/container with no such file or directory and many details " + strings.Repeat("x", 200)

	emitBulkheadPluginResult(emitter, "periodical", "cpuset_topology", "failed", rawReason)

	if len(emitter.records) != 1 {
		t.Fatalf("records = %d, want 1", len(emitter.records))
	}
	var gotReason string
	for _, tag := range emitter.records[0].tags {
		if tag.Key == "reason" {
			gotReason = tag.Val
			break
		}
	}
	wantReason := metricutil.MetricTagValueFormat(rawReason)
	if gotReason != wantReason {
		t.Fatalf("reason tag = %q, want formatted %q", gotReason, wantReason)
	}
}

func TestEmitBulkheadDesiredViewMetrics(t *testing.T) {
	t.Parallel()

	emitter := &capturingEmitter{}
	desired := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		Reserve:          machine.NewCPUSet(0),
		Dedicated:        machine.NewCPUSet(1, 2),
		SharePool:        machine.NewCPUSet(3, 4, 5),
		ReclaimEffective: machine.NewCPUSet(6, 7),
		NonReclaimPool:   machine.NewCPUSet(0, 1, 2, 3, 4, 5),
		Isolation:        machine.NewCPUSet(2),
		SharePoolMap: map[string]machine.CPUSet{
			commonstate.PoolNameShare: machine.NewCPUSet(4, 5),
		},
	}}

	emitBulkheadPartitionViewMetrics(emitter, "desired", &desired.CPUSetPartitionView)
	emitBulkheadDefaultShareResidualMetric(emitter, "desired", &desired.CPUSetPartitionView)

	want := map[string]float64{
		metricKey(metricBulkheadPartitionCPUCores, "view", "desired", "partition", "reserve"):       1,
		metricKey(metricBulkheadPartitionCPUCores, "view", "desired", "partition", "dedicated"):     2,
		metricKey(metricBulkheadPartitionCPUCores, "view", "desired", "partition", "share"):         3,
		metricKey(metricBulkheadPartitionCPUCores, "view", "desired", "partition", "reclaim"):       2,
		metricKey(metricBulkheadPartitionCPUCores, "view", "desired", "partition", "non_reclaim"):   6,
		metricKey(metricBulkheadPartitionCPUCores, "view", "desired", "partition", "isolation"):     1,
		metricKey(metricBulkheadPartitionCPUCores, "view", "desired", "partition", "default_share"): 2,
		metricKey(metricBulkheadDefaultShareResidualCPUCores, "view", "desired"):                    2,
	}
	assertMetricValues(t, emitter.records, want)
}

func TestEmitBulkheadAppliedViewMetrics(t *testing.T) {
	t.Parallel()

	emitter := &capturingEmitter{}
	desired := &model.DesiredView{CPUSetPartitionView: model.CPUSetPartitionView{
		ReclaimEffective: machine.NewCPUSet(6, 7),
		NonReclaimPool:   machine.NewCPUSet(0, 1, 2, 3, 4, 5),
		SharePoolMap: map[string]machine.CPUSet{
			commonstate.PoolNameShare: machine.NewCPUSet(4, 5),
		},
	}}
	applied := &model.AppliedView{CPUSetPartitionView: model.CPUSetPartitionView{
		ReclaimEffective: machine.NewCPUSet(7, 8),
		NonReclaimPool:   machine.NewCPUSet(0, 1, 2, 3, 4, 8),
		SharePoolMap: map[string]machine.CPUSet{
			commonstate.PoolNameShare: machine.NewCPUSet(4, 8),
		},
	}}

	emitBulkheadPartitionViewMetrics(emitter, "applied", &applied.CPUSetPartitionView)
	emitBulkheadDefaultShareResidualMetric(emitter, "applied", &applied.CPUSetPartitionView)
	emitBulkheadPartitionDiffMetrics(emitter, desired, applied)

	want := map[string]float64{
		metricKey(metricBulkheadPartitionCPUCores, "view", "applied", "partition", "reclaim"):       2,
		metricKey(metricBulkheadPartitionCPUCores, "view", "applied", "partition", "non_reclaim"):   6,
		metricKey(metricBulkheadPartitionCPUCores, "view", "applied", "partition", "default_share"): 2,
		metricKey(metricBulkheadDefaultShareResidualCPUCores, "view", "applied"):                    2,
	}
	for _, partition := range []string{"reclaim", "non_reclaim", "default_share"} {
		want[metricKey(metricBulkheadPartitionCPUDiffCores, "partition", partition, "direction", "desired_only")] = 1
		want[metricKey(metricBulkheadPartitionCPUDiffCores, "partition", partition, "direction", "applied_only")] = 1
	}
	assertMetricValues(t, emitter.records, want)
}

func metricKey(name string, tags ...string) string {
	parts := []string{name}
	for i := 0; i < len(tags); i += 2 {
		parts = append(parts, tags[i]+"="+tags[i+1])
	}
	return strings.Join(parts, ",")
}

func assertMetricValues(t *testing.T, records []capturedMetric, want map[string]float64) {
	t.Helper()
	got := make(map[string]float64, len(records))
	for _, record := range records {
		tags := make([]string, 0, len(record.tags)*2)
		for _, tag := range record.tags {
			tags = append(tags, tag.Key, tag.Val)
		}
		got[metricKey(record.key, tags...)] = record.val
	}
	for key, wantValue := range want {
		if gotValue, ok := got[key]; !ok || gotValue != wantValue {
			t.Fatalf("metric %q = %v, present=%t, want %v; all metrics: %#v", key, gotValue, ok, wantValue, got)
		}
	}
}

func hasMetric(records []capturedMetric, key string) bool {
	for _, record := range records {
		if record.key == key {
			return true
		}
	}
	return false
}

func hasMetricTags(records []capturedMetric, key string, tags ...string) bool {
	for _, record := range records {
		if record.key != key {
			continue
		}
		matched := true
		for i := 0; i < len(tags); i += 2 {
			found := false
			for _, tag := range record.tags {
				if tag.Key == tags[i] && tag.Val == tags[i+1] {
					found = true
					break
				}
			}
			if !found {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func TestBulkheadSlowHandlerThreshold(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		elapsed time.Duration
		want    bool
	}{
		{name: "below threshold", elapsed: bulkheadSlowHandlerThreshold - time.Nanosecond, want: false},
		{name: "at threshold", elapsed: bulkheadSlowHandlerThreshold, want: true},
		{name: "above threshold", elapsed: bulkheadSlowHandlerThreshold + time.Nanosecond, want: true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.elapsed >= bulkheadSlowHandlerThreshold
			if got != tt.want {
				t.Fatalf("slow classification for %s = %t, want %t", tt.elapsed, got, tt.want)
			}
		})
	}
}

func dynamicBulkheadConf(enabled bool) *dynamicconfig.Configuration {
	conf := dynamicconfig.NewConfiguration()
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable = enabled
	return conf
}

func dynamicBulkheadConfWithMinSize(enabled bool, minSize int64) *dynamicconfig.Configuration {
	conf := dynamicBulkheadConf(enabled)
	conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize = minSize
	return conf
}

func enabledCPUSetAdjustmentCtx() cpusetutil.CPUSetAdjustmentHandlerCtx {
	return cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(true),
	}
}

func enabledDynamicAgentConf() *dynamicconfig.DynamicAgentConfiguration {
	conf := dynamicconfig.NewDynamicAgentConfiguration()
	conf.SetDynamicConfiguration(dynamicBulkheadConf(true))
	return conf
}

func testBulkheadStateAndTopology() (cpustate.ReadonlyState, *machine.CPUTopology) {
	state := cpustate.NewCPUPluginState(nil)
	state.SetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult: machine.NewCPUSet(0),
	})
	state.SetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName, &cpustate.AllocationInfo{
		AllocationMeta:   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
		AllocationResult: machine.NewCPUSet(1, 2, 3),
	})
	topology := &machine.CPUTopology{
		NumCPUs:      4,
		NumCores:     4,
		NumSockets:   2,
		NumNUMANodes: 2,
		CPUDetails: machine.CPUDetails{
			0: {NUMANodeID: 0, SocketID: 0, CoreID: 0},
			1: {NUMANodeID: 0, SocketID: 0, CoreID: 1},
			2: {NUMANodeID: 1, SocketID: 1, CoreID: 2},
			3: {NUMANodeID: 1, SocketID: 1, CoreID: 3},
		},
	}
	return state, topology
}

func assertCPUSet(t *testing.T, name string, got machine.CPUSet, want string) {
	t.Helper()
	if got.String() != want {
		t.Fatalf("%s cpuset = %s, want %s", name, got.String(), want)
	}
}

func TestNewManagerRegistersDefaultPluginsInOrder(t *testing.T) {
	t.Parallel()

	m, err := NewManager(nil)
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	got := make([]string, 0, len(m.plugins))
	for _, plugin := range m.plugins {
		got = append(got, plugin.Name())
	}
	want := []string{"cpuset_topology", "cpuset_mems", "workqueue", "system_service", "rdt_cpulist", "rdt_cat", "cpu_metrics"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unexpected plugin names, got %v want %v", got, want)
	}
}

// BenchmarkManagerApply measures the Manager.Apply main dispatch path
// (lock, topology reconcile, applied-view publication, dependent plugins).
func BenchmarkManagerApply(b *testing.B) {
	topologyPlugin := &fakeDisabledTopologyPlugin{
		fakePlugin:      &fakePlugin{name: "cpuset_topology", enabled: true},
		shouldReconcile: true,
		results: []fakeDAGResult{
			reclaimOnlyResult(machine.NewCPUSet(1, 2)),
		},
	}
	dependent := &fakePlugin{name: "dependent", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	in := enabledCPUSetAdjustmentCtx()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := m.Apply(context.Background(), in); err != nil {
			b.Fatalf("Apply: %v", err)
		}
	}
}

// deferredTopologyPlugin is a fake topology owner that reports a deliberate
// zero-write hold (ConvergenceLevelDeferred) on Apply.
type deferredTopologyPlugin struct {
	*fakePlugin
	applyCalls int
}

func (p *deferredTopologyPlugin) Apply(_ context.Context, _ bulkheadapi.HandlerContext) (bulkheadapi.TopologyOutcome, error) {
	p.applyCalls++
	return bulkheadapi.TopologyOutcome{Level: bulkheadapi.ConvergenceLevelDeferred, Note: "shadow hold"}, nil
}

// TestManagerDeferredHoldPreservesOldAppliedView proves a Deferred topology
// outcome keeps the previous AppliedView/revision, skips dependent plugins and
// publishes nothing, and surfaces the typed ErrTopologyDeferred.
func TestManagerDeferredHoldPreservesOldAppliedView(t *testing.T) {
	t.Parallel()
	topologyPlugin := &deferredTopologyPlugin{fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true}}
	dependent := &fakePlugin{name: "dependent", enabled: true}
	old := reclaimOnlyResult(machine.NewCPUSet(3)).AppliedView
	oldRevision := uint64(9)
	m := &Manager{
		plugins:             []bulkheadapi.Plugin{topologyPlugin, dependent},
		appliedView:         old,
		appliedViewRevision: oldRevision,
	}
	m.publishLatestAppliedReclaim(old.ReclaimEffective)
	in := enabledCPUSetAdjustmentCtx()

	_, err := m.Apply(context.Background(), in)
	if !errors.Is(err, ErrTopologyDeferred) {
		t.Fatalf("Apply() error = %v, want errors.Is(ErrTopologyDeferred)", err)
	}
	if topologyPlugin.applyCalls != 1 {
		t.Fatalf("topology apply calls = %d, want 1", topologyPlugin.applyCalls)
	}
	if len(dependent.adjustViews) != 0 {
		t.Fatalf("dependent plugin calls = %d, want 0", len(dependent.adjustViews))
	}
	if m.appliedView != old {
		t.Fatalf("applied view pointer changed; want old view preserved")
	}
	if m.appliedViewRevision != oldRevision {
		t.Fatalf("applied revision = %d, want %d unchanged", m.appliedViewRevision, oldRevision)
	}
	assertCPUSet(t, "retained latest reclaim", m.LatestAppliedReclaim(), "3")
}

// TestManagerDeferredHoldRevisionZeroProof proves the bootstrap case (no prior
// AppliedView, revision 0) also returns ErrTopologyDeferred without running
// dependent workqueue/system consumers and without panicking.
func TestManagerDeferredHoldRevisionZeroProof(t *testing.T) {
	t.Parallel()
	topologyPlugin := &deferredTopologyPlugin{fakePlugin: &fakePlugin{name: "cpuset_topology", enabled: true}}
	dependent := &fakePlugin{name: "dependent", enabled: true}
	m := &Manager{plugins: []bulkheadapi.Plugin{topologyPlugin, dependent}}
	in := enabledCPUSetAdjustmentCtx()

	_, err := m.Apply(context.Background(), in)
	if !errors.Is(err, ErrTopologyDeferred) {
		t.Fatalf("Apply() error = %v, want errors.Is(ErrTopologyDeferred)", err)
	}
	if len(dependent.adjustViews) != 0 {
		t.Fatalf("dependent plugin calls = %d, want 0 on revision0", len(dependent.adjustViews))
	}
	if m.appliedView != nil || m.appliedViewRevision != 0 {
		t.Fatalf("bootstrap must not seed AppliedView: view=%v revision=%d", m.appliedView, m.appliedViewRevision)
	}
}

type fakeGlobalOffSysfsRestorer struct {
	mask     string
	restored bool
}

func (f *fakeGlobalOffSysfsRestorer) Name() string { return "fake-global-off-sysfs" }
func (f *fakeGlobalOffSysfsRestorer) Enable(bulkheadapi.HandlerContext) bool {
	return true
}

func (f *fakeGlobalOffSysfsRestorer) RestoreSysfsOnGlobalOff(context.Context, bulkheadapi.HandlerContext) error {
	f.mask = "full"
	f.restored = true
	return nil
}

// TestManagerGlobalOffRestoresSysfsSideEffects pins a sysfs side-effect (modeled
// on the workqueue cpumask) while bulkhead is on, then flips the global switch
// off and calls Manager.Apply. Pre-fix the global-off short-circuit withheld the
// restore and the mask stayed at the reclaim union; post-fix the idempotent
// GlobalOffSysfsRestorer runs and restores the full mask, without enabling
// topology or cgroup disabled handlers.
func TestManagerGlobalOffRestoresSysfsSideEffects(t *testing.T) {
	t.Parallel()
	restorer := &fakeGlobalOffSysfsRestorer{mask: "reclaim-union"}
	m := &Manager{
		plugins:     []bulkheadapi.Plugin{restorer},
		appliedView: &model.AppliedView{CPUSetPartitionView: model.NewCPUSetPartitionView()},
	}
	in := cpusetutil.CPUSetAdjustmentHandlerCtx{
		DynamicConf: dynamicBulkheadConf(false),
	}
	if _, err := m.Apply(context.Background(), in); err != nil {
		t.Fatalf("Apply with global bulkhead off returned error: %v", err)
	}
	if !restorer.restored {
		t.Fatalf("GlobalOffSysfsRestorer not invoked on global-off short-circuit; mask left at %q", restorer.mask)
	}
	if restorer.mask != "full" {
		t.Fatalf("restored mask = %q, want full", restorer.mask)
	}
}
