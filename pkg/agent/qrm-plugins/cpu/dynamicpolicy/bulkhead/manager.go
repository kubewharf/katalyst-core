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
	"fmt"
	"strconv"
	"sync"
	"time"

	apierrors "k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	bulkheadapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/api"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/registry"
	bulkheadutils "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/utils"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	bulkheadconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/bulkhead"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	cgroupclient "github.com/kubewharf/katalyst-core/pkg/util/cgroup/client"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	metricutil "github.com/kubewharf/katalyst-core/pkg/util/metric"
)

type Manager struct {
	mu                            cancelableMutex
	latestAppliedReclaimMu        sync.RWMutex
	plugins                       []bulkheadapi.Plugin
	defaultNonReclaimPoolMinSize  int64
	lastCPUSetAdjustmentEnabled   map[string]bool
	appliedView                   *model.AppliedView
	appliedViewRevision           uint64
	appliedViewValidForPeriodical bool
	latestAppliedReclaim          machine.CPUSet

	// topologyOwner is the at-most-one plugin that converges topology. In
	// production it implements both TopologyPlugin and DisabledTopologyReconciler;
	// tests may split the two interfaces across fakes, so discovery accepts a
	// plugin carrying either. It is resolved by interface assertion (never by
	// name) and cached. Zero owners is valid (topology convergence disabled);
	// two or more distinct owners is fatal.
	topologyOwner      bulkheadapi.Plugin
	topologyDiscovered bool
}

// cancelableMutex is a zero-value-ready binary semaphore. Unlike sync.Mutex,
// acquisition can stop when the caller's context expires.
//
// Semantics (identical to sync.Mutex for ownership tracking):
//   - Unlock MUST only be called after a successful Lock (one that returned nil).
//     Unlock without a prior successful Lock blocks forever (the buffer-1 channel
//     already holds its token from init), mirroring sync.Mutex's double-unlock
//     panic. Callers defer Unlock only after Lock succeeds.
//   - If Lock times out on ctx.Done(), the token was NOT consumed; the caller
//     returns without deferring Unlock, so no token leak occurs.
//   - The once.Do lazy init means the zero value is immediately usable; no
//     constructor is required.
type cancelableMutex struct {
	once  sync.Once
	token chan struct{}
}

func (m *cancelableMutex) init() {
	m.once.Do(func() {
		m.token = make(chan struct{}, 1)
		m.token <- struct{}{}
	})
}

func (m *cancelableMutex) Lock(ctx context.Context) error {
	m.init()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-m.token:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *cancelableMutex) Unlock() {
	m.init()
	m.token <- struct{}{}
}

// NonConvergedError reports a retryable topology outcome that must not be
// treated as successful Bulkhead apply or authorize dependent plugins.
type NonConvergedError struct {
	Note string
}

func (e *NonConvergedError) Error() string {
	return "bulkhead topology not fully converged: " + e.Note
}

// ErrTopologyDeferred is the typed sentinel the manager returns when the
// topology owner reports ConvergenceLevelDeferred (a deliberate zero-write
// hold). Callers classify it via errors.Is as a retry/defer rather than a
// terminal admission rejection.
var ErrTopologyDeferred = errors.New("bulkhead topology deferred zero-write hold")

const (
	metricBulkheadHandlerResult                = "bulkhead_handler_result"
	metricBulkheadViewChanged                  = "bulkhead_view_changed"
	metricBulkheadPartitionCPUCores            = "bulkhead_partition_cpu_cores"
	metricBulkheadPartitionCPUDiffCores        = "bulkhead_partition_cpu_diff_cores"
	metricBulkheadDefaultShareResidualCPUCores = "bulkhead_default_share_residual_cpu_cores"
	bulkheadSlowHandlerThreshold               = 500 * time.Millisecond
)

// applyLoopAccumulator groups the per-round mutable state threaded through the
// plugin execution loop. It is created fresh at the start of every Apply call
// and replaces the handful of bare local variables that were previously shared
// between the loop body, the topology callback, and the publish phase.
type applyLoopAccumulator struct {
	anyAdjusted       bool
	topologyPublished bool
	topologyStopped   bool
	topologyApplied   bool
	verifiedReclaim   machine.CPUSet
	outcome           bulkheadapi.TopologyOutcome
}

// preparedApply carries everything Apply needs after the preparation phase.
// desiredDefer, when non-nil, must be deferred by Apply so the "desired" view
// metrics are emitted on every return path, matching the original inline defer.
// shortCircuitDisabledGate, when true, tells Apply to return the empty CPUSet
// with a nil error (the global bulkhead-disabled gate).
type preparedApply struct {
	handlerCtx               *bulkheadapi.HandlerContext
	acc                      *applyLoopAccumulator
	currentEnabled           map[string]bool
	desiredSnapshot          *model.DesiredView
	desiredDefer             func()
	shortCircuitDisabledGate bool
}

type bulkheadPartitionMetricDescriptor struct {
	name     string
	cpuSet   func(*model.CPUSetPartitionView) machine.CPUSet
	emitDiff bool
}

var bulkheadPartitionMetricDescriptors = []bulkheadPartitionMetricDescriptor{
	{name: "reserve", cpuSet: func(view *model.CPUSetPartitionView) machine.CPUSet { return view.Reserve }},
	{name: "dedicated", cpuSet: func(view *model.CPUSetPartitionView) machine.CPUSet { return view.Dedicated }},
	{name: "share", cpuSet: func(view *model.CPUSetPartitionView) machine.CPUSet { return view.SharePool }},
	{name: "reclaim", cpuSet: func(view *model.CPUSetPartitionView) machine.CPUSet { return view.ReclaimEffective }, emitDiff: true},
	{name: "non_reclaim", cpuSet: func(view *model.CPUSetPartitionView) machine.CPUSet { return view.NonReclaimPool }, emitDiff: true},
	{name: "isolation", cpuSet: func(view *model.CPUSetPartitionView) machine.CPUSet { return view.Isolation }},
	{name: "default_share", cpuSet: func(view *model.CPUSetPartitionView) machine.CPUSet {
		return view.SharePoolMap[commonstate.PoolNameShare]
	}, emitDiff: true},
}

func NewManager(conf *config.Configuration) (*Manager, error) {
	return NewManagerWithCgroupClientFactory(conf, nil)
}

// NewManagerWithCgroupClientFactory builds a bulkhead manager with an injectable
// cgroup client factory. A nil factory defaults to cgroupclient.NewCgroupClient.
func NewManagerWithCgroupClientFactory(conf *config.Configuration, cgroupClientFactory cgroupclient.CgroupClientFactory) (*Manager, error) {
	plugins, err := registry.NewDefaultPluginsWithCgroupClient(conf, cgroupClientFactory)
	if err != nil {
		return nil, err
	}
	var defaultNonReclaimPoolMinSize int64
	if conf != nil && conf.DynamicAgentConfiguration != nil {
		defaultConf := conf.DynamicAgentConfiguration.GetDynamicConfiguration()
		defaultNonReclaimPoolMinSize = bulkheadNonReclaimPoolMinSize(defaultConf)
	}
	m := &Manager{
		plugins:                      plugins,
		defaultNonReclaimPoolMinSize: defaultNonReclaimPoolMinSize,
	}
	if err := m.discoverTopologyPlugin(); err != nil {
		return nil, err
	}
	return m, nil
}

// discoverTopologyPlugin resolves the single topology-owning plugin from the
// registry by interface assertion. It never compares plugin names. Zero owners
// is permitted; two or more distinct owners is a configuration error.
func (m *Manager) discoverTopologyPlugin() error {
	if m.topologyDiscovered {
		return nil
	}
	var found []bulkheadapi.Plugin
	for _, p := range m.plugins {
		_, isTopology := p.(bulkheadapi.TopologyPlugin)
		_, isReconciler := p.(bulkheadapi.DisabledTopologyReconciler)
		if isTopology || isReconciler {
			found = append(found, p)
		}
	}
	switch len(found) {
	case 0:
	case 1:
		m.topologyOwner = found[0]
	default:
		names := make([]string, 0, len(found))
		for _, tp := range found {
			names = append(names, tp.Name())
		}
		return fmt.Errorf("bulkhead manager expects at most one topology owner, got %d: %v", len(found), names)
	}
	m.topologyDiscovered = true
	return nil
}

func (m *Manager) RunCPUSetAdjustmentHandlers(ctx context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) error {
	_, err := m.Apply(ctx, in)
	return err
}

// Apply converges topology before running partition-dependent plugins and
// returns the reclaim CPUSet verified by the topology layer's final snapshot.
//
// The work is split into three phases: prepareApply validates inputs and builds
// the per-round handler context, executeApply runs the plugin loop, and
// publishApply commits the converged view and decides the return value. The
// manager lock, the apply-finished logging defer, and the "desired" metrics
// defer stay here so their ordering and late binding match the original.
func (m *Manager) Apply(ctx context.Context, in cpusetutil.CPUSetAdjustmentHandlerCtx) (out machine.CPUSet, err error) {
	start := time.Now()
	var topologyAppliedLog bool
	var topologyPublishedLog bool
	var anyAdjustedLog bool
	defer func() {
		general.Infof("bulkhead: manager apply finished duration=%s err=%v any_adjusted=%t topology_applied=%t topology_published=%t generation=%d",
			time.Since(start), err, anyAdjustedLog, topologyAppliedLog, topologyPublishedLog, in.Generation)
	}()

	if err := m.mu.Lock(ctx); err != nil {
		return machine.NewCPUSet(), fmt.Errorf("acquire bulkhead manager lock: %w", err)
	}
	defer m.mu.Unlock()
	if err := m.discoverTopologyPlugin(); err != nil {
		return machine.NewCPUSet(), err
	}
	disabledRoundStart := time.Now()
	empty := machine.NewCPUSet()

	prepared, prepErr := m.prepareApply(in)
	if prepErr != nil {
		return empty, prepErr
	}
	if prepared.desiredDefer != nil {
		defer prepared.desiredDefer()
	}
	if prepared.shortCircuitDisabledGate {
		return empty, nil
	}

	if execErr := m.executeApply(ctx, in, prepared.handlerCtx, prepared.acc, prepared.currentEnabled, prepared.desiredSnapshot, disabledRoundStart); execErr != nil {
		return empty, execErr
	}

	resultCPUs, pubErr := m.publishApply(in, prepared.handlerCtx, prepared.acc, prepared.currentEnabled)
	if pubErr != nil {
		return empty, pubErr
	}
	topologyAppliedLog = prepared.acc.topologyApplied
	topologyPublishedLog = prepared.acc.topologyPublished
	anyAdjustedLog = prepared.acc.anyAdjusted
	return resultCPUs, nil
}

func shouldProjectPendingToDesiredNonReclaim(
	mode cpusetutil.CPUSetAdjustmentMode,
	hardPartition, allowOverlap bool,
) bool {
	// RampUp hard-partition no longer disables pending projection: the per-NUMA
	// reclaim floor must coexist with the admission of shared ramp-up pods. Only
	// share/reclaim overlap disables it. hardPartition is retained as an explicit
	// (now non-gating) axis so the call site and tests keep naming the config.
	return mode.OrFullDefault() == cpusetutil.CPUSetAdjustmentModeAdmission &&
		!allowOverlap
}

// prepareApply builds the per-round handler context, resolves ramp-up domains,
// applies the global bulkhead-disabled gate, and constructs the topology result
// callback. It returns the prepared state plus an optional desired-view metrics
// defer and an optional short-circuit CPUSet for the disabled-gate path.
func (m *Manager) prepareApply(in cpusetutil.CPUSetAdjustmentHandlerCtx) (*preparedApply, error) {
	prepared := &preparedApply{
		handlerCtx: &bulkheadapi.HandlerContext{
			CPUSetAdjustmentHandlerCtx: in,
			AppliedView:                m.appliedView.DeepCopy(),
			AppliedViewRevision:        m.appliedViewRevision,
		},
		acc: &applyLoopAccumulator{
			verifiedReclaim: machine.NewCPUSet(),
		},
	}
	if !commitIfGenerationCurrent(in, func() {
		m.appliedViewValidForPeriodical = false
	}) {
		return nil, staleGenerationError()
	}
	rampUpDomains, dErr := m.activeRampUpDomains(in)
	if dErr != nil {
		return nil, fmt.Errorf("resolve active ramp-up domains for bulkhead apply: %w", dErr)
	}
	viewOptions := m.cpuSetPartitionViewOptions(in, rampUpDomains)
	statePresent := in.State != nil
	stateAllowOverlap := statePresent && in.State.GetAllowSharedCoresOverlapReclaimedCores()
	prepared.handlerCtx.ProjectPendingToDesiredNonReclaim = shouldProjectPendingToDesiredNonReclaim(
		in.Mode, viewOptions.HardPartitionEnabled, stateAllowOverlap)
	// DIAGNOSTIC ONLY: record every conjunct that decides
	// ProjectPendingToDesiredNonReclaim at its exact construction site. The 738
	// pre-Converge trace only emits the final bool inside the topology plugin, so a
	// gate that should be ON in a soft, non-overlap admission cannot be attributed
	// to mode / hardPartition / allowOverlap. HardPartition reads the live
	// DynamicConf (EnableReclaim && EnableRampUpReclaimHardPartition, gated on
	// rampUpDomains.Len()>0); allowOverlap reads the committed cpu-plugin STATE
	// flag, which is fed from the advisor-published CPUAdvice over the proto
	// channel and can lag the live DynamicConf. Read-only: never selects a hold,
	// mutates the view, or feeds the write path.
	dynEnableReclaim, dynRampUpHardPartition := false, false
	if in.DynamicConf != nil &&
		in.DynamicConf.AdminQoSConfiguration != nil &&
		in.DynamicConf.AdminQoSConfiguration.CPUPluginConfiguration != nil {
		dynEnableReclaim = in.DynamicConf.EnableReclaim
		dynRampUpHardPartition = in.DynamicConf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition
	}
	general.InfofV(4,
		"bulkhead: project-pending gate construction generation=%d mode=%q mode_effective=%q rampup_domains=%d dyn_enable_reclaim=%t dyn_rampup_hard_partition=%t computed_hard_partition=%t state_present=%t state_allow_overlap=%t state_disable_dedicated=%t final_project_pending_to_desired_non_reclaim=%t",
		in.Generation, in.Mode, in.Mode.OrFullDefault(), rampUpDomains.Len(),
		dynEnableReclaim, dynRampUpHardPartition, viewOptions.HardPartitionEnabled,
		statePresent, stateAllowOverlap,
		statePresent && in.State.GetDisableDedicatedCoresOverlapReclaimedCores(),
		prepared.handlerCtx.ProjectPendingToDesiredNonReclaim)
	if !bulkheadEnabled(in.DynamicConf) {
		// The global bulkhead switch is a hard gate: when it is off, do not run
		// plugin Enable/adjust/disabled handlers. Disabled handlers may write
		// cgroup or sysfs rollback state, which is still bulkhead-owned behavior
		// and can introduce unexpected changes after the user explicitly turns
		// bulkhead off.
		if !commitIfGenerationCurrent(in, func() {
			m.lastCPUSetAdjustmentEnabled = nil
		}) {
			return nil, staleGenerationError()
		}
		// Restore idempotent sysfs side-effects (e.g. workqueue cpumask) to their
		// unmanaged default even though the global switch is off. Only plugins that
		// explicitly opt in via GlobalOffSysfsRestorer run here; topology and cgroup
		// disabled handlers remain withheld by the short-circuit below.
		for _, p := range m.plugins {
			restorer, ok := p.(bulkheadapi.GlobalOffSysfsRestorer)
			if !ok {
				continue
			}
			if err := restorer.RestoreSysfsOnGlobalOff(context.Background(), *prepared.handlerCtx); err != nil {
				emitBulkheadPluginResult(prepared.handlerCtx.Emitter, "global_off_sysfs_restore", p.Name(), "failed", err.Error())
			}
		}
		emitBulkheadViewChanged(prepared.handlerCtx.Emitter, false)
		prepared.shortCircuitDisabledGate = true
		return prepared, nil
	}
	if in.State != nil {
		desiredView, pendingSharedProjection, err := bulkheadutils.BuildValidatedCPUSetPartitionView(in.State, in.Topology, viewOptions)
		if err != nil {
			return nil, fmt.Errorf("build bulkhead desired view failed: %w", err)
		}
		prepared.handlerCtx.DesiredView = desiredView
		// Transient planning hint only: kept off DesiredView so it never affects
		// DeepCopy, view equality, revision, or checkpoint. The live rebuild is
		// the authoritative superset (all currently-pending shared RampUp pods);
		// the admission override only adds containers whose admission-snapshot
		// state is not yet visible in the live state. Union, never replace, so a
		// partial override never drops another pending shared pod.
		hint := pendingSharedProjection
		if ov := in.CommitOverride; ov != nil && len(ov.PendingSharedProjection) > 0 {
			hint = bulkheadutils.UnionPendingSharedProjection(pendingSharedProjection, ov.PendingSharedProjection)
		}
		prepared.handlerCtx.PendingSharedProjection = hint
		prepared.handlerCtx.View = desiredView.CPUSetPartitionView.DeepCopy()
		if in.Mode.OrFullDefault() == cpusetutil.CPUSetAdjustmentModeAdmission {
			general.InfofV(4,
				"bulkhead: admission partition view generation=%d reclaim_raw=%s desired_reclaim=%s applied_reclaim=%s desired_non_reclaim=%s pending_shared_projection=%d project_pending_to_desired_non_reclaim=%t",
				in.Generation, desiredView.ReclaimRaw.String(), desiredView.DesiredReclaimEffective.String(),
				desiredView.ReclaimEffective.String(), desiredView.DesiredNonReclaimPool.String(), len(hint),
				prepared.handlerCtx.ProjectPendingToDesiredNonReclaim)
		}
		hc := prepared.handlerCtx
		prepared.desiredDefer = func() {
			emitBulkheadPartitionViewMetrics(hc.Emitter, "desired", &hc.DesiredView.CPUSetPartitionView)
			if defaultShareResidualEnabled(in.DynamicConf) {
				emitBulkheadDefaultShareResidualMetric(hc.Emitter, "desired", &hc.DesiredView.CPUSetPartitionView)
			}
		}
	}
	prepared.currentEnabled = m.buildPluginEnabledState(*prepared.handlerCtx)
	prepared.desiredSnapshot = prepared.handlerCtx.DesiredView.DeepCopy()
	return prepared, nil
}

// executeApply runs the plugin loop: it reconciles disabled plugins, runs the
// topology plugin, and then runs the remaining adjustment-capable plugins. It
// mutates the accumulator and handler context in place and returns the first
// terminal error. Every early return in the original loop returned the empty
// CPUSet together with the error, so collapsing them to a bare error preserves
// behavior.
func (m *Manager) executeApply(
	ctx context.Context,
	in cpusetutil.CPUSetAdjustmentHandlerCtx,
	handlerCtx *bulkheadapi.HandlerContext,
	acc *applyLoopAccumulator,
	currentEnabled map[string]bool,
	desiredSnapshot *model.DesiredView,
	disabledRoundStart time.Time,
) error {
	if m.topologyOwner != nil {
		if err := m.processApplyPlugin(ctx, in, handlerCtx, m.topologyOwner, acc, currentEnabled, desiredSnapshot, disabledRoundStart); err != nil {
			return err
		}
	}
	for _, p := range m.plugins {
		if p == m.topologyOwner {
			continue
		}
		if err := m.processApplyPlugin(ctx, in, handlerCtx, p, acc, currentEnabled, desiredSnapshot, disabledRoundStart); err != nil {
			return err
		}
	}
	return nil
}

// processApplyPlugin runs one plugin's adjustment round. See executeApply.
func (m *Manager) processApplyPlugin(
	ctx context.Context,
	in cpusetutil.CPUSetAdjustmentHandlerCtx,
	handlerCtx *bulkheadapi.HandlerContext,
	p bulkheadapi.Plugin,
	acc *applyLoopAccumulator,
	currentEnabled map[string]bool,
	desiredSnapshot *model.DesiredView,
	disabledRoundStart time.Time,
) error {
	// processApplyPlugin executes one plugin's disabled or enabled adjustment
	// round. It is called twice per round: first for the topology owner (if any),
	// then for all other plugins in registry order. Call order, generation fences,
	// and gating are unchanged from the previous single loop.
	if !commitIfGenerationCurrent(in, func() {}) {
		return staleGenerationError()
	}
	if !currentEnabled[p.Name()] {
		if reconciler, ok := p.(bulkheadapi.DisabledTopologyReconciler); ok &&
			reconciler.ShouldReconcileWhenDisabled(ctx, *handlerCtx) {
			disabledCtx, cancel := context.WithDeadline(ctx, disabledRoundStart.Add(managerTopologyDeadline(in.CoreConf)))
			if reconciler.NeedsDisabledReset() {
				if !commitIfGenerationCurrent(in, func() {}) {
					cancel()
					return staleGenerationError()
				}
				adjuster, ok := p.(bulkheadapi.AdjustmentCapable)
				if !ok {
					cancel()
					return fmt.Errorf("bulkhead plugin %q is a disabled-topology reconciler without adjustment capability", p.Name())
				}
				// A required reset runs before the reclaim-only reconcile. Any error it
				// returns is propagated to the caller here, before ReconcileDisabled;
				// the reset-complete marker and the later view publish are independent
				// generation-fence commits.
				err := adjuster.CPUSetAdjustmentDisabledHandler(disabledCtx, *handlerCtx)
				if err != nil {
					cancel()
					emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment_disabled", p.Name(), "failed", err.Error())
					return fmt.Errorf("bulkhead plugin %q disabled transition failed: %w", p.Name(), err)
				}
				// First generation-fence commit: record reset completion. This is
				// intentionally separate from the view publish that follows, so a
				// generation advance during ReconcileDisabled leaves the reset marked
				// complete while the unpublished view is retried next round.
				if !commitIfGenerationCurrent(in, func() {
					reconciler.MarkDisabledResetComplete()
				}) {
					cancel()
					return staleGenerationError()
				}
				emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment_disabled", p.Name(), "success", "")
				acc.anyAdjusted = true
			}
			topologyCtx := *handlerCtx
			outcome, err := reconciler.ReconcileDisabled(disabledCtx, topologyCtx)
			cancel()
			if !commitIfGenerationCurrent(in, func() {}) {
				return staleGenerationError()
			}
			if err != nil {
				emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "failed", err.Error())
				return fmt.Errorf("bulkhead plugin %q disabled reconciliation failed: %w", p.Name(), err)
			}
			if outcome.Level == bulkheadapi.ConvergenceLevelNone {
				nonConverged := &NonConvergedError{Note: outcome.Note}
				emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "failed", nonConverged.Error())
				return nonConverged
			}
			if err := m.acceptTopologyOutcome(in, handlerCtx, p.Name(), outcome, desiredSnapshot, acc, "after disabled topology reconcile"); err != nil {
				return err
			}
			return nil
		}
		// Disabled and not reconciling this round: run the disabled reset only
		// when the plugin itself says it still needs one (transition into disabled
		// mode, or a retried/authoritative exit reset).
		needsReset := false
		if reconciler, ok := p.(bulkheadapi.DisabledTopologyReconciler); ok {
			needsReset = reconciler.NeedsDisabledReset()
		} else if _, ok := p.(bulkheadapi.AdjustmentCapable); ok {
			needsReset = m.needsDisabledReset(p.Name())
		}
		if !needsReset {
			if p == m.topologyOwner {
				acc.topologyStopped = true
			}
			return nil
		}
		var err error
		if adjuster, ok := p.(bulkheadapi.AdjustmentCapable); ok {
			err = adjuster.CPUSetAdjustmentDisabledHandler(ctx, *handlerCtx)
		}
		markComplete := func() {}
		if reconciler, ok := p.(bulkheadapi.DisabledTopologyReconciler); ok && err == nil {
			markComplete = reconciler.MarkDisabledResetComplete
		}
		if !commitIfGenerationCurrent(in, markComplete) {
			return staleGenerationError()
		}
		if err != nil {
			// Only the topology owner's deferred-only reset may be swallowed: it
			// converged its controlled targets and merely deferred uncertain
			// descendants, so the caller proceeds and the reset retries later. A
			// non-owner plugin surfacing this sentinel is a contract violation and
			// must fail closed rather than hold the whole adjustment round.
			if errors.Is(err, bulkheadapi.ErrDisabledResetDeferred) && p == m.topologyOwner {
				// A deferred-only reset deliberately left uncertain descendants
				// untouched. Because err is non-nil the fence above ran the no-op
				// markComplete: the reset is not recorded complete, so
				// NeedsDisabledReset stays true. No topology view is accepted this
				// round (topologyStopped). The retry is opportunistic, not a timer:
				// it runs on the next adjustment that enters Manager.Apply — the next
				// Allocate admission or a new advisor post-commit round. Periodical
				// handlers do not re-run the disabled reset. The caller proceeds so
				// it may itself materialize or retire the descendant blocking completion.
				acc.topologyStopped = true
				emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment_disabled", p.Name(), "deferred", err.Error())
				return nil
			}
			emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment_disabled", p.Name(), "failed", err.Error())
			return fmt.Errorf("bulkhead plugin %q disabled transition failed: %w", p.Name(), err)
		}
		emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment_disabled", p.Name(), "success", "")
		acc.anyAdjusted = true
		if p == m.topologyOwner {
			acc.topologyStopped = true
		}
		return nil
	}
	if acc.topologyStopped {
		return nil
	}
	if topologyPlugin, ok := p.(bulkheadapi.TopologyPlugin); ok {
		topologyCtx := *handlerCtx
		// A full topology attempt may write part of the hierarchy before it
		// fails. Mark the owner as requiring an authoritative disabled reset
		// before starting the attempt, rather than only after convergence.
		if !commitIfGenerationCurrent(in, func() {
			if m.lastCPUSetAdjustmentEnabled == nil {
				m.lastCPUSetAdjustmentEnabled = make(map[string]bool)
			}
			m.lastCPUSetAdjustmentEnabled[p.Name()] = true
		}) {
			return staleGenerationError()
		}
		outcome, err := topologyPlugin.Apply(ctx, topologyCtx)
		if !commitIfGenerationCurrent(in, func() {}) {
			return staleGenerationError()
		}
		if err != nil {
			emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "failed", err.Error())
			return fmt.Errorf("bulkhead plugin %q cpuset adjustment failed: %w", p.Name(), err)
		}
		if outcome.Level == bulkheadapi.ConvergenceLevelNone {
			nonConverged := &NonConvergedError{Note: outcome.Note}
			emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "failed", nonConverged.Error())
			return nonConverged
		}
		// Deferred is a deliberate zero-write hold: keep the previous
		// AppliedView, do NOT accept/publish a new view or checkpoint, and stop
		// dependent plugins (workqueue/system) that would interpret an empty
		// reclaim set. Surface an identifiable retry error to the caller.
		if outcome.Level == bulkheadapi.ConvergenceLevelDeferred {
			acc.topologyStopped = true
			emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "deferred", outcome.Note)
			return fmt.Errorf("bulkhead topology %w: %s", ErrTopologyDeferred, outcome.Note)
		}
		// Rebuild desired intent after topology Apply so a result cannot be
		// accepted, or authorize dependent side effects, after state changed.
		if err := m.acceptTopologyOutcome(in, handlerCtx, p.Name(), outcome, desiredSnapshot, acc, "after topology apply"); err != nil {
			return err
		}
		if outcome.Level == bulkheadapi.ConvergenceLevelPartial {
			// A partial (parent-safe) view proves partition/reclaim safety
			// only. Do not authorize dependent plugins that may require exact
			// leaf state.
			acc.topologyStopped = true
		}
		return nil
	}
	var err error
	if adjuster, ok := p.(bulkheadapi.AdjustmentCapable); ok {
		err = adjuster.CPUSetAdjustmentHandler(ctx, *handlerCtx)
	}
	if !commitIfGenerationCurrent(in, func() {}) {
		return staleGenerationError()
	}
	if err != nil {
		emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "failed", err.Error())
		return fmt.Errorf("bulkhead plugin %q cpuset adjustment failed: %w", p.Name(), err)
	}
	emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", p.Name(), "success", "")
	acc.anyAdjusted = true
	if p == m.topologyOwner {
		if !acc.topologyPublished {
			acc.topologyStopped = true
			return nil
		}
	}
	return nil
}

// acceptConvergedTopologyResult is shared by the disabled-reconcile and the
// full topology-apply success paths. It rebuilds the desired intent, rejects the
// result when the intent drifted, validates the hard partition, and commits the
// converged applied view into the per-round handler context and accumulator.
// phase is embedded in the rebuild error strings to preserve the original
// messages.
func (m *Manager) acceptTopologyOutcome(
	in cpusetutil.CPUSetAdjustmentHandlerCtx,
	handlerCtx *bulkheadapi.HandlerContext,
	pluginName string,
	outcome bulkheadapi.TopologyOutcome,
	desiredSnapshot *model.DesiredView,
	acc *applyLoopAccumulator,
	phase string,
) error {
	if desiredSnapshot != nil {
		currentRampUpDomains, dErr := m.activeRampUpDomains(in)
		if dErr != nil {
			return fmt.Errorf("resolve active ramp-up domains %s: %w", phase, dErr)
		}
		currentDesired, _, err := bulkheadutils.BuildValidatedCPUSetPartitionView(
			in.State,
			in.Topology,
			m.cpuSetPartitionViewOptions(in, currentRampUpDomains),
		)
		if err != nil {
			return fmt.Errorf("rebuild bulkhead desired view %s failed: %w", phase, err)
		}
		if !model.EqualDesiredView(currentDesired, desiredSnapshot) {
			nonConverged := &NonConvergedError{Note: "desired view drifted " + phase}
			emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", pluginName, "failed", nonConverged.Error())
			return nonConverged
		}
	}
	if err := m.validateAppliedHardPartition(in, outcome.View); err != nil {
		return fmt.Errorf("validate bulkhead topology applied view: %w", err)
	}
	handlerCtx.AppliedView = outcome.View.DeepCopy()
	handlerCtx.View = handlerCtx.AppliedView.CPUSetPartitionView.DeepCopy()
	acc.verifiedReclaim = handlerCtx.AppliedView.ReclaimEffective.Clone()
	handlerCtx.AppliedViewRevision = m.appliedViewRevision
	if !model.EqualAppliedView(m.appliedView, handlerCtx.AppliedView) {
		handlerCtx.AppliedViewRevision++
	}
	acc.outcome = outcome
	acc.topologyApplied = true
	acc.topologyPublished = true
	acc.anyAdjusted = true
	emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", pluginName, "success", "")
	return nil
}

// publishApply commits the converged applied view (or merely the enabled-state
// map when topology did not run), emits the applied metrics, and decides the
// reclaim CPUSet to return. The caller records the apply-finished log flags
// from the accumulator after a successful publish.
// publishApply is the appliedView commit boundary for one adjustment pass.
//
// appliedView advances only inside commitIfGenerationCurrent: the fence checks
// that in.Generation is still the current generation before running the commit
// callback. If a newer adjustment pass (or a reset) started while this pass was
// applying, the callback is skipped and publishApply returns a
// staleGenerationError, so appliedView never reflects a plan that has already
// been superseded. Within the winning generation, the appliedView snapshot, its
// revision and the periodical-valid flag are mutated together as one commit; a
// non-full convergence leaves appliedViewValidForPeriodical=false so the next
// periodical reconcile re-derives state instead of trusting a half-converged view.
func (m *Manager) publishApply(
	in cpusetutil.CPUSetAdjustmentHandlerCtx,
	handlerCtx *bulkheadapi.HandlerContext,
	acc *applyLoopAccumulator,
	currentEnabled map[string]bool,
) (machine.CPUSet, error) {
	empty := machine.NewCPUSet()
	if acc.topologyApplied {
		if !commitIfGenerationCurrent(in, func() {
			m.appliedView = handlerCtx.AppliedView.DeepCopy()
			m.appliedViewRevision = handlerCtx.AppliedViewRevision
			m.appliedViewValidForPeriodical = acc.outcome.Level == bulkheadapi.ConvergenceLevelFull
			m.publishLatestAppliedReclaim(acc.verifiedReclaim)
			m.lastCPUSetAdjustmentEnabled = currentEnabled
		}) {
			nonConverged := staleGenerationError()
			emitBulkheadPluginResult(handlerCtx.Emitter, "cpuset_adjustment", "generation_fence", "failed", nonConverged.Error())
			return empty, nonConverged
		}
		emitBulkheadPartitionViewMetrics(handlerCtx.Emitter, "applied", &handlerCtx.AppliedView.CPUSetPartitionView)
		emitBulkheadPartitionDiffMetrics(handlerCtx.Emitter, handlerCtx.DesiredView, handlerCtx.AppliedView)
		if defaultShareResidualEnabled(in.DynamicConf) {
			emitBulkheadDefaultShareResidualMetric(handlerCtx.Emitter, "applied", &handlerCtx.AppliedView.CPUSetPartitionView)
		}
	} else {
		if !commitIfGenerationCurrent(in, func() {
			m.lastCPUSetAdjustmentEnabled = currentEnabled
		}) {
			return empty, staleGenerationError()
		}
	}
	emitBulkheadViewChanged(handlerCtx.Emitter, acc.anyAdjusted)
	if acc.topologyApplied {
		if acc.outcome.Level == bulkheadapi.ConvergenceLevelFull && handlerCtx.CommitOverride != nil && !acc.verifiedReclaim.IsEmpty() {
			handlerCtx.CommitOverride.ReclaimEffective = acc.verifiedReclaim.Clone()
			handlerCtx.CommitOverride.Source = m.topologyOwner.Name()
		}
		return acc.verifiedReclaim.Clone(), nil
	}
	if acc.topologyPublished && m.appliedView != nil {
		return m.appliedView.ReclaimEffective.Clone(), nil
	}
	return empty, nil
}

func (m *Manager) publishLatestAppliedReclaim(cpus machine.CPUSet) {
	m.latestAppliedReclaimMu.Lock()
	defer m.latestAppliedReclaimMu.Unlock()
	m.latestAppliedReclaim = cpus.Clone()
}

func (m *Manager) LatestAppliedReclaim() machine.CPUSet {
	m.latestAppliedReclaimMu.RLock()
	defer m.latestAppliedReclaimMu.RUnlock()
	return m.latestAppliedReclaim.Clone()
}

// commitIfGenerationCurrent runs the commit callback only if the in.Generation
// this pass observed is still the current generation held by the caller.
//
// Fence invariant: the commit callback (which mutates appliedView, its revision,
// and the periodical-valid flag) runs atomically under the caller's generation
// lock. If a newer adjustment pass or a reset started while this pass was
// applying, the callback is skipped and false is returned, so appliedView never
// reflects a plan that has already been superseded. When
// in.CommitIfGenerationCurrent is nil (e.g. unit tests), the fence is a no-op
// and the callback always runs.
func commitIfGenerationCurrent(in cpusetutil.CPUSetAdjustmentHandlerCtx, commit func()) bool {
	if in.CommitIfGenerationCurrent == nil {
		commit()
		return true
	}
	return in.CommitIfGenerationCurrent(in.Generation, commit)
}

func staleGenerationError() *NonConvergedError {
	return &NonConvergedError{Note: "stale generation fence"}
}

// activeRampUpDomains resolves the ramp-up reclaim domains recorded in the
// current pod entries. A resolution error is propagated to the caller: silently
// collapsing to an empty set would disable the hard-partition reclaim floor on
// ambiguous state, which is under-protection rather than a safe fallback.
func (m *Manager) activeRampUpDomains(in cpusetutil.CPUSetAdjustmentHandlerCtx) (sets.Int, error) {
	if in.State == nil || in.Topology == nil {
		return sets.NewInt(), nil
	}
	return in.State.GetPodEntries().ActiveRampUpDomains(in.Topology)
}

func (m *Manager) cpuSetPartitionViewOptions(
	in cpusetutil.CPUSetAdjustmentHandlerCtx,
	rampUpDomains sets.Int,
) bulkheadutils.CPUSetPartitionViewOptions {
	opts := bulkheadutils.NewCPUSetPartitionViewOptionsWithState(
		in.CoreConf,
		in.DynamicConf,
		in.Topology,
		bulkheadutils.CPUSetPartitionViewState{
			State:                         in.State,
			ReservedCPUs:                  in.ReservedCPUs,
			ReservedReclaimedCPUs:         in.ReservedReclaimedCPUs,
			ReservedReclaimedCPUsFallback: in.ReservedReclaimedCPUsSize,
		},
		rampUpDomains,
	)
	if opts.NonReclaimPoolMinSize <= 0 {
		opts.NonReclaimPoolMinSize = m.defaultNonReclaimPoolMinSize
	}
	return opts
}

func (m *Manager) validateAppliedHardPartition(
	in cpusetutil.CPUSetAdjustmentHandlerCtx,
	applied *model.AppliedView,
) error {
	if applied != nil &&
		applied.Level == model.AppliedViewLevelReclaimOnly &&
		applied.ReclaimEffective.IsEmpty() {
		return nil
	}
	rampUpDomains, dErr := m.activeRampUpDomains(in)
	if dErr != nil {
		return fmt.Errorf("resolve active ramp-up domains for hard-partition validation: %w", dErr)
	}
	opts := m.cpuSetPartitionViewOptions(in, rampUpDomains)
	if opts.HardPartitionTargetError != nil {
		return opts.HardPartitionTargetError
	}
	if !opts.HardPartitionEnabled {
		return nil
	}
	if applied == nil {
		return fmt.Errorf("missing applied view")
	}
	return bulkheadutils.ValidateHardPartitionAppliedView(applied, in.State, in.Topology, opts)
}

func (m *Manager) buildPluginEnabledState(in bulkheadapi.HandlerContext) map[string]bool {
	out := make(map[string]bool, len(m.plugins))
	for _, p := range m.plugins {
		out[p.Name()] = p.Enable(in)
	}
	return out
}

// needsDisabledReset reports whether a currently-disabled plugin should run its
// disabled reset handler. A nil lastCPUSetAdjustmentEnabled means we have no
// prior state (e.g. after restart) and must reset once to converge.
func (m *Manager) needsDisabledReset(name string) bool {
	return m.lastCPUSetAdjustmentEnabled == nil || m.lastCPUSetAdjustmentEnabled[name]
}

func bulkheadEnabled(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.Enable
}

func bulkheadNonReclaimPoolMinSize(conf *dynamicconfig.Configuration) int64 {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return 0
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize
}

func (m *Manager) RunPeriodicalHandlers(
	coreConf *config.Configuration,
	extraConf interface{},
	dynamicConf *dynamicconfig.DynamicAgentConfiguration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
) {
	ctx, cancel := context.WithTimeout(context.Background(), managerHandlerTimeout(coreConf))
	defer cancel()
	if err := m.mu.Lock(ctx); err != nil {
		_ = general.UpdateHealthzStateByError(cpuconsts.SyncBulkhead, err)
		general.ErrorS(err, "bulkhead periodical handlers failed to acquire manager lock")
		return
	}
	defer m.mu.Unlock()

	// Start timing after acquiring m.mu so the slow-handler log reflects the
	// actual handler execution time rather than lock-contention wait, which
	// would otherwise inflate elapsed and produce misleading slow warnings.
	started := time.Now()
	var err error
	defer func() {
		elapsed := time.Since(started)
		if elapsed >= bulkheadSlowHandlerThreshold {
			general.InfofV(2, "bulkhead periodical handlers slow elapsed=%s", elapsed)
		}
		_ = general.UpdateHealthzStateByError(cpuconsts.SyncBulkhead, err)
		if err != nil {
			general.ErrorS(err, "bulkhead periodical handlers failed")
		}
	}()

	var conf *dynamicconfig.Configuration
	if dynamicConf != nil {
		conf = dynamicConf.GetDynamicConfiguration()
	}
	if !bulkheadEnabled(conf) {
		// Keep the periodical path behind the same hard global gate as the
		// cpuset adjustment path. Periodical handlers may reconcile external
		// resources such as cpuset partitions or workqueue masks, so running them
		// while bulkhead is globally disabled would still mutate bulkhead-owned
		// state.
		return
	}
	handlerCtx := bulkheadapi.PeriodicalHandlerContext{
		CoreConf:                      coreConf,
		ExtraConf:                     extraConf,
		DynamicConf:                   conf,
		Emitter:                       emitter,
		MetaServer:                    metaServer,
		AppliedViewValidForPeriodical: m.appliedViewValidForPeriodical,
	}
	if m.appliedViewValidForPeriodical {
		handlerCtx.AppliedView = m.appliedView.DeepCopy()
		handlerCtx.AppliedViewRevision = m.appliedViewRevision
	}
	var errs []error
	for _, p := range m.plugins {
		pluginCtx := handlerCtx
		if enabled, ok := m.lastCPUSetAdjustmentEnabled[p.Name()]; ok {
			pluginCtx.EffectiveEnabled = &enabled
		}
		handlerStarted := time.Now()
		var pluginErr error
		if periodical, ok := p.(bulkheadapi.PeriodicalCapable); ok {
			pluginErr = periodical.PeriodicalHandler(ctx, pluginCtx)
		}
		handlerElapsed := time.Since(handlerStarted)
		if handlerElapsed >= bulkheadSlowHandlerThreshold {
			general.InfofV(2, "bulkhead periodical slow plugin=%s elapsed=%s", p.Name(), handlerElapsed)
		}
		if pluginErr != nil {
			wrapped := fmt.Errorf("bulkhead plugin %q periodical failed: %w", p.Name(), pluginErr)
			general.ErrorS(wrapped, "bulkhead periodical handler failed")
			emitBulkheadPluginResult(emitter, "periodical", p.Name(), "failed", pluginErr.Error())
			errs = append(errs, wrapped)
			continue
		}
		emitBulkheadPluginResult(emitter, "periodical", p.Name(), "success", "")
	}
	err = apierrors.NewAggregate(errs)
}

func managerHandlerTimeout(coreConf *config.Configuration) time.Duration {
	if coreConf == nil || coreConf.CPUQRMPluginConfig == nil {
		return bulkheadconfig.TopologyHandlerTimeout(nil)
	}
	return bulkheadconfig.TopologyHandlerTimeout(coreConf.CPUQRMPluginConfig.BulkheadConfiguration)
}

func managerTopologyDeadline(coreConf *config.Configuration) time.Duration {
	if coreConf == nil || coreConf.CPUQRMPluginConfig == nil ||
		coreConf.CPUQRMPluginConfig.BulkheadConfiguration == nil {
		return bulkheadconfig.DefaultTopologyConvergenceDeadline
	}
	deadline := coreConf.CPUQRMPluginConfig.BulkheadConfiguration.TopologyConvergenceBudget.DeadlineDuration
	if deadline <= 0 {
		return bulkheadconfig.DefaultTopologyConvergenceDeadline
	}
	return deadline
}

func emitBulkheadPluginResult(emitter metrics.MetricEmitter, phase, plugin, status, reason string) {
	if emitter == nil {
		return
	}
	_ = emitter.StoreInt64(metricBulkheadHandlerResult, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "phase", Val: phase},
		metrics.MetricTag{Key: "plugin", Val: plugin},
		metrics.MetricTag{Key: "status", Val: status},
		metrics.MetricTag{Key: "reason", Val: metricutil.MetricTagValueFormat(reason)},
	)
}

func emitBulkheadViewChanged(emitter metrics.MetricEmitter, changed bool) {
	if emitter == nil {
		return
	}
	_ = emitter.StoreInt64(metricBulkheadViewChanged, 1, metrics.MetricTypeNameCount,
		metrics.MetricTag{Key: "changed", Val: strconv.FormatBool(changed)},
	)
}

func emitBulkheadPartitionViewMetrics(
	emitter metrics.MetricEmitter,
	viewName string,
	view *model.CPUSetPartitionView,
) {
	if emitter == nil || view == nil {
		return
	}
	for _, descriptor := range bulkheadPartitionMetricDescriptors {
		_ = emitter.StoreInt64(metricBulkheadPartitionCPUCores, int64(descriptor.cpuSet(view).Size()), metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "view", Val: viewName},
			metrics.MetricTag{Key: "partition", Val: descriptor.name},
		)
	}
}

func emitBulkheadDefaultShareResidualMetric(
	emitter metrics.MetricEmitter,
	viewName string,
	view *model.CPUSetPartitionView,
) {
	if emitter == nil || view == nil {
		return
	}
	_ = emitter.StoreInt64(metricBulkheadDefaultShareResidualCPUCores,
		int64(view.SharePoolMap[commonstate.PoolNameShare].Size()), metrics.MetricTypeNameRaw,
		metrics.MetricTag{Key: "view", Val: viewName},
	)
}

func defaultShareResidualEnabled(conf *dynamicconfig.Configuration) bool {
	return conf != nil && conf.FillDefaultSharePoolWithNonReclaimCPUs
}

func emitBulkheadPartitionDiffMetrics(
	emitter metrics.MetricEmitter,
	desired *model.DesiredView,
	applied *model.AppliedView,
) {
	if emitter == nil || desired == nil || applied == nil {
		return
	}
	for _, descriptor := range bulkheadPartitionMetricDescriptors {
		if !descriptor.emitDiff {
			continue
		}
		desiredCPUSet := descriptor.cpuSet(&desired.CPUSetPartitionView)
		appliedCPUSet := descriptor.cpuSet(&applied.CPUSetPartitionView)
		_ = emitter.StoreInt64(metricBulkheadPartitionCPUDiffCores,
			int64(desiredCPUSet.Difference(appliedCPUSet).Size()), metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "partition", Val: descriptor.name},
			metrics.MetricTag{Key: "direction", Val: "desired_only"},
		)
		_ = emitter.StoreInt64(metricBulkheadPartitionCPUDiffCores,
			int64(appliedCPUSet.Difference(desiredCPUSet).Size()), metrics.MetricTypeNameRaw,
			metrics.MetricTag{Key: "partition", Val: descriptor.name},
			metrics.MetricTag{Key: "direction", Val: "applied_only"},
		)
	}
}
