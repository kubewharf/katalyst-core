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

package dynamicpolicy

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"
	maputil "k8s.io/kubernetes/pkg/util/maps"
	"k8s.io/utils/clock"

	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-api/pkg/plugins/skeleton"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent"
	"github.com/kubewharf/katalyst-core/cmd/katalyst-agent/app/agent/qrm"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/accompanyresource"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/advisorsvc"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead"
	advisorapi "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpuadvisor"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/cpueviction"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/registry"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/irqtuner"
	irqtuingcontroller "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/irqtuner/controller"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	cpusetutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/validator"
	cpuutil "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/featuregatenegotiation"
	"github.com/kubewharf/katalyst-core/pkg/agent/utilcomponent/periodicalhandler"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic/crd"
	"github.com/kubewharf/katalyst-core/pkg/config/generic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	podmeta "github.com/kubewharf/katalyst-core/pkg/metaserver/agent/pod"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/resourcepackage"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	"github.com/kubewharf/katalyst-core/pkg/util/metric"
	"github.com/kubewharf/katalyst-core/pkg/util/process"
	"github.com/kubewharf/katalyst-core/pkg/util/reclaim"
	"github.com/kubewharf/katalyst-core/pkg/util/timemonitor"
)

type allocationPodMetaContextKey struct{}

type cpuSetAdjustmentExecutionLeaseContextKey struct{}

type cpuSetAdjustmentExecutionLease struct {
	policy    *DynamicPolicy
	execution chan struct{}
	mu        sync.Mutex
	active    bool
}

func (l *cpuSetAdjustmentExecutionLease) release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.active {
		return
	}
	l.active = false
	<-l.execution
}

func (l *cpuSetAdjustmentExecutionLease) isActiveFor(
	policy *DynamicPolicy,
	execution chan struct{},
) bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active && l.policy == policy && l.execution == execution
}

func podMetaFromResourceRequest(req *pluginapi.ResourceRequest) metav1.ObjectMeta {
	podMeta := (&metav1.ObjectMeta{
		UID:         types.UID(req.PodUid),
		Namespace:   req.PodNamespace,
		Name:        req.PodName,
		Labels:      req.Labels,
		Annotations: req.Annotations,
	}).DeepCopy()
	return *podMeta
}

func withAllocationPodMeta(ctx context.Context, req *pluginapi.ResourceRequest) context.Context {
	return context.WithValue(ctx, allocationPodMetaContextKey{}, podMetaFromResourceRequest(req))
}

func allocationPodMetaFromContext(ctx context.Context) (metav1.ObjectMeta, bool) {
	podMeta, ok := ctx.Value(allocationPodMetaContextKey{}).(metav1.ObjectMeta)
	return podMeta, ok
}

const (
	cpuPluginStateFileName = "cpu_plugin_state"

	reservedReclaimedCPUsSize = 4

	cpusetCheckPeriod             = 10 * time.Second
	stateCheckPeriod              = 30 * time.Second
	maxResidualTime               = 5 * time.Minute
	syncCPUIdlePeriod             = 30 * time.Second
	syncContainerCPUIdlePeriod    = 30 * time.Second
	syncCPUBurstPeriod            = 10 * time.Second
	syncSystemExclusivePoolPeriod = 10 * time.Second
	syncCPUWeightPeriod           = 10 * time.Second
	syncBulkheadPeriod            = 30 * time.Second
	rampUpTransitionPeriod        = 30 * time.Second

	healthCheckTolerationTimes = 3
)

var (
	AccompanyResourceRegistry = accompanyresource.NewRegistry()
	packAllocationResponse    = cpuutil.PackAllocationResponse
)

// accompanyRegistryOps is the subset of accompanyresource.Registry used by the
// dynamic policy. A nil DynamicPolicy.accompanyRegistry falls back to the
// package-global AccompanyResourceRegistry, preserving production behavior.
type accompanyRegistryOps interface {
	GetAccompanyResourceTopologyHints(req *pluginapi.ResourceRequest, hints *pluginapi.ListOfTopologyHints) error
	AllocateAccompanyResource(req *pluginapi.ResourceRequest, resp *pluginapi.ResourceAllocationResponse) error
	ReleaseAccompanyResource(req *pluginapi.RemovePodRequest) error
}

// accompanyReg returns the per-instance accompany registry when installed,
// otherwise the package-global default.
func (p *DynamicPolicy) accompanyReg() accompanyRegistryOps {
	if p.accompanyRegistry != nil {
		return p.accompanyRegistry
	}
	return AccompanyResourceRegistry
}

// packResp returns the per-instance packer when installed, otherwise the
// package-global production packer.
func (p *DynamicPolicy) packResp(allocationInfo *state.AllocationInfo, resourceName, ociPropertyName string,
	isNodeResource, isScalarResource bool, req *pluginapi.ResourceRequest,
	resourceAllocationAnnotations ...map[string]string,
) (*pluginapi.ResourceAllocationResponse, error) {
	if p.packAllocationFn != nil {
		return p.packAllocationFn(allocationInfo, resourceName, ociPropertyName,
			isNodeResource, isScalarResource, req, resourceAllocationAnnotations...)
	}
	return packAllocationResponse(allocationInfo, resourceName, ociPropertyName,
		isNodeResource, isScalarResource, req, resourceAllocationAnnotations...)
}

func rampUpDeadlineReached(initTime time.Time, transitionPeriod time.Duration, now time.Time) bool {
	return !now.Before(initTime.Add(transitionPeriod))
}

func shouldAllocationFinishRampUp(
	allocationInfo *state.AllocationInfo,
	transitionPeriod time.Duration,
	now time.Time,
) (bool, error) {
	if allocationInfo == nil || !allocationInfo.RampUp {
		return false, nil
	}
	initTime, err := time.Parse(util.QRMTimeFormat, allocationInfo.InitTimestamp)
	if err != nil {
		return false, err
	}
	return rampUpDeadlineReached(initTime, transitionPeriod, now), nil
}

// AllocationHook is a hook function which can be registered and called when allocationInfo changes.
// It is designed to intercept state updates and perform actions like injecting or updating annotations
// (e.g., NUMA topology information) based on the differences between old and new allocation info.
type AllocationHook func(oldAllocationInfo, newAllocationInfo *state.AllocationInfo) error

type allocationRequestKey struct {
	podUID        string
	containerName string
}

type allocationRequestLock struct {
	sync.Mutex
	refCount int
}

type allocationRollbackSnapshot struct {
	revision                uint64
	podEntries              state.PodEntries
	machineState            state.NUMANodeMap
	allowOverlap            bool
	disableDedicatedOverlap bool
}

// advisorComponent groups the CPU advisor connection, request validation,
// health-monitor and feature-gate lifecycle fields of DynamicPolicy. It is a
// pure field grouping with no behavior or signature change: every access moves
// from p.<field> to p.advisor.<field>, and field types/order are unchanged.
type advisorComponent struct {
	advisorClient      advisorapi.CPUAdvisorClient
	advisorConn        *grpc.ClientConn
	advisorValidator   *validator.CPUAdvisorValidator
	advisorMonitor     *timemonitor.TimeMonitor
	featureGateManager featuregatenegotiation.FeatureGateManager
}

// machineComponent groups the machine topology reference of DynamicPolicy. It
// is a pure field grouping with no behavior or signature change: every access
// moves from p.machine.machineInfo to p.machine.machineInfo.
type machineComponent struct {
	machineInfo *machine.KatalystMachineInfo
}

// metaComponent groups the metadata server reference of DynamicPolicy. It is a
// pure field grouping with no behavior or signature change: every access moves
// from p.meta.metaServer to p.meta.metaServer.
type metaComponent struct {
	metaServer *metaserver.MetaServer
}

// emitterComponent groups the metrics emitter reference of DynamicPolicy. It is
// a pure field grouping with no behavior or signature change: every access moves
// from p.emitter.emitter to p.emitter.emitter.emitter.
type emitterComponent struct {
	emitter metrics.MetricEmitter
}

// reclaimComponent groups the reserved reclaim-pool fields of DynamicPolicy. It
// is a pure field grouping with no behavior or signature change: every access
// moves from p.<field> to p.reclaim.<field>.
type reclaimComponent struct {
	reservedReclaimedCPUsSize                 int
	reservedReclaimedCPUSet                   machine.CPUSet
	reservedReclaimedTopologyAwareAssignments map[int]machine.CPUSet
	reclaimConsumersForKCNR                   []string
}

// configComponent groups the configuration-parsed fields of DynamicPolicy. It is
// a pure field grouping with no behavior or signature change: every access moves
// from p.<field> to p.config.<field>. Field order is preserved.
// bulkheadComponent groups the bulkhead manager reference of DynamicPolicy.
// hintOptimizerComponent groups the shared/dedicated NUMABinding hint optimizers.
type hintOptimizerComponent struct {
	sharedCoresNUMABindingHintOptimizer    hintoptimizer.HintOptimizer
	dedicatedCoresNUMABindingHintOptimizer hintoptimizer.HintOptimizer
}

type bulkheadComponent struct {
	bulkheadManager *bulkhead.Manager
}

type configComponent struct {
	enableCPUAdvisor                          bool
	getAdviceInterval                         time.Duration
	reservedCPUs                              machine.CPUSet
	cpuAdvisorSocketAbsPath                   string
	cpuPluginSocketAbsPath                    string
	extraStateFileAbsPath                     string
	enableReclaimNUMABinding                  bool
	enableSNBHighNumaPreference               bool
	enableCPUIdle                             bool
	enableSyncingCPUIdle                      bool
	enableContainerCPUIdle                    bool
	enableCPUBurst                            bool
	reclaimRelativeRootCgroupPaths            []string
	numaBindingReclaimRelativeRootCgroupPaths map[int][]string
	qosConfig                                 *generic.QoSConfiguration
	dynamicConfig                             *dynamicconfig.DynamicAgentConfiguration
	conf                                      *config.Configuration
	podDebugAnnoKeys                          []string
	podAnnotationKeptKeys                     []string
	podLabelKeptKeys                          []string
	numaBindingResultAnnotationKey            string
	numaNumberAnnotationKey                   string
	numaIDsAnnotationKey                      string
	topologyAllocationAnnotationKey           string
	transitionPeriod                          time.Duration
}

// adjustmentComponent groups the CPU set adjustment retry dispatcher together
// with the advisor post-commit target state machine of DynamicPolicy. These
// fields share a single mutex (cpuSetAdjustmentRetryMu) and are mutated together
// by the cpuset adjustment/post-commit methods in cpuset_adjustment_handler.go
// (the post-commit target drives the adjustment retry loop). It is a pure field
// grouping with no behavior or signature change: every access moves from
// p.<field> to p.adjustment.<field>. Field types and order are unchanged.
type adjustmentComponent struct {
	cpuSetAdjustmentHandlers      map[string]cpusetutil.CPUSetAdjustmentHandler
	cpuSetAdjustmentExecution     chan struct{}
	cpuSetAdjustmentRetryMu       sync.Mutex
	cpuSetAdjustmentRetryQueued   bool
	cpuSetAdjustmentRetryAgain    bool
	cpuSetAdjustmentRetryDirty    bool
	cpuSetAdjustmentRetryReasons  map[cpusetutil.CPUSetAdjustmentRetryReason]struct{}
	cpuSetAdjustmentRetryPersist  bool
	cpuSetAdjustmentRetryStopCh   <-chan struct{}
	cpuSetAdjustmentRetryStopping bool
	cpuSetAdjustmentRetryWG       sync.WaitGroup
	cpuSetAdjustmentGeneration    uint64
	advisorPostCommitTarget       *advisorPostCommitTarget
	advisorPostCommitTargetChange chan struct{}
	advisorStateWritePermits      map[*state.WritePermit]*advisorPostCommitTarget
	// stagedPendingSharedProjection carries the per-container pending shared
	// projection hint computed synchronously on the admission/commit snapshot
	// into the next adjustment round's commit override. It is transient, off
	// DesiredView, and cleared once consumed by an admission-mode round.
	stagedPendingSharedProjection map[string]map[string]struct{}
}

// allocationComponent groups the CPU allocation dispatch tables, allocation
// hooks, and the per-request lock registry of DynamicPolicy. These are all read
// or written only during allocation request handling in policy.go. It is a
// pure field grouping with no behavior or signature change: every access moves
// from p.<field> to p.allocation.<field>. Field types and order are unchanged.
type allocationComponent struct {
	allocationHandlers       map[string]util.AllocationHandler
	hintHandlers             map[string]util.HintHandler
	allocationHooks          []AllocationHook
	allocationRequestLocksMu sync.Mutex
	allocationRequestLocks   map[allocationRequestKey]*allocationRequestLock
}

// DynamicPolicy is the policy that's used by default;
// it will consider the dynamic running information to calculate
// and adjust resource requirements and configurations
type DynamicPolicy struct {
	sync.RWMutex
	pluginapi.UnimplementedResourcePluginServer

	name    string
	stopCh  chan struct{}
	started bool

	emitter emitterComponent
	meta    metaComponent
	machine machineComponent

	advisor advisorComponent

	advisorapi.UnimplementedCPUPluginServer

	state                          state.State
	residualHitMap                 map[string]int64
	allocation                     allocationComponent
	adjustment                     adjustmentComponent
	advisorPostCommitCheckpointDir string
	steadyFakeNUMAMigration        *steadyFakeNUMAMigrationIntentStore

	cpuPressureEviction       agent.Component
	cpuPressureEvictionCancel context.CancelFunc

	resourcePackageManager *resourcepackage.CachedResourcePackageManager

	irqTuner irqtuner.Tuner
	bulkhead bulkheadComponent

	// those are parsed from configurations
	// todo if we want to use dynamic configuration, we'd better not use self-defined conf
	config configComponent

	reclaim reclaimComponent

	hintOpt hintOptimizerComponent

	// post-commit apply stage seams. A nil hook means the method runs its
	// production implementation; this keeps the zero-value DynamicPolicy (and the
	// production constructor) bound to the original behavior. Tests override a
	// hook on their own freshly-built instance to record calls or force a stage
	// failure without monkey patching, which is what lets these tests run under
	// t.Parallel.
	applyHeadroomHook               func(resp *advisorapi.ListAndWatchResponse) error
	applyCgroupConfigsHook          func(resp *advisorapi.ListAndWatchResponse) error
	runCPUSetAdjustmentHandlersHook func(ctx context.Context, modes ...cpusetutil.CPUSetAdjustmentMode) error
	markAdvisorApplyFailedHook      func(revision uint64)

	// allocation-adjustment seams. A nil hook runs the production method; tests
	// override on their own instance to force a specific adjustment outcome.
	adjustAtRevisionWithContextHook              func(ctx context.Context, entries state.PodEntries, machineState state.NUMANodeMap, persistCheckpoint bool, expectedRevision uint64) error
	adjustForRecoveryAtRevisionWithContextHook   func(ctx context.Context, entries state.PodEntries, machineState state.NUMANodeMap, persistCheckpoint bool, expectedRevision uint64) error
	adjustAfterDeletionAtRevisionHook            func(entries state.PodEntries, machineState state.NUMANodeMap, persistCheckpoint bool, expectedRevision uint64) error
	adjustAfterDeletionAtRevisionWithContextHook func(ctx context.Context, entries state.PodEntries, machineState state.NUMANodeMap, persistCheckpoint bool, expectedRevision uint64) error

	// periodical-handler registration seam. A nil hook calls the production
	// periodicalhandler.RegisterPeriodicalHandlerWithHealthz; tests override it to
	// force a registration error without touching the global handler registry.
	registerPeriodicalHandlerHook func(handlerName string, initState general.HealthzCheckState, groupName string,
		handler periodicalhandler.Handler, interval time.Duration, tolerationTimes int64) error

	// accompanyRegistry, when non-nil, overrides the package-global
	// AccompanyResourceRegistry for this instance. Tests set it to an isolated
	// registry so parallel subtests do not race on the package global.
	accompanyRegistry accompanyRegistryOps

	// packAllocationFn, when non-nil, overrides the package-global
	// packAllocationResponse for this instance. Tests set it to a failing/recording
	// packer so parallel subtests do not race on the package global.
	packAllocationFn func(allocationInfo *state.AllocationInfo, resourceName, ociPropertyName string,
		isNodeResource, isScalarResource bool, req *pluginapi.ResourceRequest,
		resourceAllocationAnnotations ...map[string]string,
	) (*pluginapi.ResourceAllocationResponse, error)

	// cgroupOps holds the per-instance cgroup/path/runtime calls for the cgroup-v1
	// quota helpers. A nil pointer lazily binds to the production defaults.
	cgroupOps *cgroupResourceOps

	// cpuSetHealthOps holds the per-instance cgroup reads for checkCPUSet.
	cpuSetHealthOps *cpuSetHealthOps
}

// registerPeriodicalHandler routes periodical handler registration through the
// test seam when installed, otherwise to the production global registry.
func (p *DynamicPolicy) registerPeriodicalHandler(handlerName string, initState general.HealthzCheckState, groupName string,
	handler periodicalhandler.Handler, interval time.Duration, tolerationTimes int64,
) error {
	if p.registerPeriodicalHandlerHook != nil {
		return p.registerPeriodicalHandlerHook(handlerName, initState, groupName, handler, interval, tolerationTimes)
	}
	return periodicalhandler.RegisterPeriodicalHandlerWithHealthz(handlerName, initState, groupName, handler, interval, tolerationTimes)
}

func (p *DynamicPolicy) lockAllocationRequest(podUID, containerName string) func() {
	key := allocationRequestKey{podUID: podUID, containerName: containerName}

	p.allocation.allocationRequestLocksMu.Lock()
	if p.allocation.allocationRequestLocks == nil {
		p.allocation.allocationRequestLocks = make(map[allocationRequestKey]*allocationRequestLock)
	}
	requestLock := p.allocation.allocationRequestLocks[key]
	if requestLock == nil {
		requestLock = &allocationRequestLock{}
		p.allocation.allocationRequestLocks[key] = requestLock
	}
	requestLock.refCount++
	p.allocation.allocationRequestLocksMu.Unlock()

	requestLock.Lock()
	return func() {
		requestLock.Unlock()

		p.allocation.allocationRequestLocksMu.Lock()
		requestLock.refCount--
		if requestLock.refCount == 0 && p.allocation.allocationRequestLocks[key] == requestLock {
			delete(p.allocation.allocationRequestLocks, key)
		}
		p.allocation.allocationRequestLocksMu.Unlock()
	}
}

func (p *DynamicPolicy) rollbackAllocationState(
	req *pluginapi.ResourceRequest,
	snapshot allocationRollbackSnapshot,
) error {
	if p.state.GetRevision() == snapshot.revision {
		return nil
	}

	err := p.state.CommitAdvisorStateIfRevision(
		snapshot.revision,
		snapshot.podEntries,
		snapshot.machineState,
		snapshot.allowOverlap,
		snapshot.disableDedicatedOverlap,
		true,
	)
	if err == nil {
		return nil
	}
	if !errors.Is(err, state.ErrStaleStateRevision) {
		return fmt.Errorf("restore allocation state: %w", err)
	}

	currentRevision := p.state.GetRevision()
	currentPodEntries := p.state.GetPodEntries()
	rollbackAllocationInfo := snapshot.podEntries[req.PodUid][req.ContainerName]
	if rollbackAllocationInfo != nil {
		if currentPodEntries[req.PodUid] == nil {
			currentPodEntries[req.PodUid] = make(state.ContainerEntries)
		}
		currentPodEntries[req.PodUid][req.ContainerName] = rollbackAllocationInfo.Clone()
	} else if currentPodEntries[req.PodUid] != nil {
		delete(currentPodEntries[req.PodUid], req.ContainerName)
		if len(currentPodEntries[req.PodUid]) == 0 {
			delete(currentPodEntries, req.PodUid)
		}
	}

	currentMachineState, err := generateMachineStateFromPodEntries(
		p.machine.machineInfo.CPUTopology,
		currentPodEntries,
		p.state.GetMachineState(),
	)
	if err != nil {
		return fmt.Errorf("generate machine state for stale allocation rollback: %w", err)
	}

	allowOverlap := p.state.GetAllowSharedCoresOverlapReclaimedCores()
	disableDedicatedOverlap := p.state.GetDisableDedicatedCoresOverlapReclaimedCores()
	planningState := state.NewTransientState(p.machine.machineInfo.CPUTopology)
	if err := planningState.CommitAdvisorState(
		currentPodEntries,
		currentMachineState,
		allowOverlap,
		disableDedicatedOverlap,
		false,
	); err != nil {
		return fmt.Errorf("initialize stale allocation rollback planning state: %w", err)
	}

	planningPolicy := p.newRampUpPlanningPolicy(planningState)
	if err := planningPolicy.adjustAllocationEntriesWithRampUpFloorForModeAtRevision(
		currentPodEntries,
		currentMachineState,
		false,
		machine.NewCPUSet(),
		false,
		planningState.GetRevision(),
		defaultShareMaterializationRecovery,
	); err != nil {
		return fmt.Errorf("replan pools for stale allocation rollback: %w", err)
	}

	if err := p.state.CommitAdvisorStateIfRevision(
		currentRevision,
		planningState.GetPodEntries(),
		planningState.GetMachineState(),
		allowOverlap,
		disableDedicatedOverlap,
		true,
	); err != nil {
		return fmt.Errorf("persist stale allocation rollback: %w", err)
	}
	return nil
}

func NewDynamicPolicy(agentCtx *agent.GenericContext, conf *config.Configuration,
	_ interface{}, agentName string,
) (bool, agent.Component, error) {
	// add watcher for general gvrs needed in most cases
	reservedCPUs, reserveErr := cpuutil.GetCoresReservedForSystem(conf, agentCtx.MetaServer, agentCtx.KatalystMachineInfo, agentCtx.CPUDetails.CPUs().Clone())
	if reserveErr != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("GetCoresReservedForSystem for reservedCPUsNum: %d, reservedCPUList: %s failed with error: %v",
			conf.ReservedCPUCores, conf.ReservedCPUList, reserveErr)
	}

	wrappedEmitter := agentCtx.EmitterPool.GetDefaultMetricsEmitter().WithTags(agentName, metrics.MetricTag{
		Key: util.QRMPluginPolicyTagName,
		Val: cpuconsts.CPUResourcePluginPolicyNameDynamic,
	})

	stateImpl, stateErr := state.NewCheckpointState(conf.StateDirectoryConfiguration, cpuPluginStateFileName,
		cpuconsts.CPUResourcePluginPolicyNameDynamic, agentCtx.CPUTopology, conf.SkipCPUStateCorruption, state.GenerateMachineStateFromPodEntries, wrappedEmitter)
	if stateErr != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("NewCheckpointState failed with error: %v", stateErr)
	}

	var (
		cpuPressureEviction agent.Component
		err                 error
	)
	if conf.EnableCPUPressureEviction {
		cpuPressureEviction, err = cpueviction.NewCPUPressureEviction(
			agentCtx.EmitterPool.GetDefaultMetricsEmitter(), agentCtx.MetaServer, conf, stateImpl)
		if err != nil {
			return false, agent.ComponentStub{}, err
		}
	}
	bulkheadManager, err := bulkhead.NewManager(conf)
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy init bulkhead manager failed with error: %v", err)
	}

	// since the reservedCPUs won't influence stateImpl directly.
	// so we don't modify stateImpl with reservedCPUs here.
	// for those pods have already been allocated reservedCPUs,
	// we won't touch them and wait them to be deleted the next update.
	policyImplement := &DynamicPolicy{
		name:   fmt.Sprintf("%s_%s", agentName, cpuconsts.CPUResourcePluginPolicyNameDynamic),
		stopCh: make(chan struct{}),

		resourcePackageManager: resourcepackage.NewCachedResourcePackageManager(agentCtx.MetaServer.ResourcePackageManager),

		state:          stateImpl,
		residualHitMap: make(map[string]int64),

		advisor: advisorComponent{
			advisorValidator:   validator.NewCPUAdvisorValidator(stateImpl, agentCtx.KatalystMachineInfo),
			featureGateManager: featuregatenegotiation.NewFeatureGateManager(conf),
		},

		cpuPressureEviction: cpuPressureEviction,

		machine: machineComponent{machineInfo: agentCtx.KatalystMachineInfo},
		meta:    metaComponent{metaServer: agentCtx.MetaServer},
		emitter: emitterComponent{emitter: wrappedEmitter},
		reclaim: reclaimComponent{
			reservedReclaimedCPUsSize: general.Max(reservedReclaimedCPUsSize, agentCtx.KatalystMachineInfo.NumNUMANodes),
			reclaimConsumersForKCNR:   conf.ReclaimConsumersForKCNR,
		},
		config: configComponent{
			conf:                           conf,
			qosConfig:                      conf.QoSConfiguration,
			dynamicConfig:                  conf.DynamicAgentConfiguration,
			cpuAdvisorSocketAbsPath:        conf.CPUAdvisorSocketAbsPath,
			cpuPluginSocketAbsPath:         conf.CPUPluginSocketAbsPath,
			enableReclaimNUMABinding:       conf.EnableReclaimNUMABinding,
			enableSNBHighNumaPreference:    conf.EnableSNBHighNumaPreference,
			enableCPUAdvisor:               conf.CPUQRMPluginConfig.EnableCPUAdvisor,
			getAdviceInterval:              conf.CPUQRMPluginConfig.GetAdviceInterval,
			reservedCPUs:                   reservedCPUs,
			extraStateFileAbsPath:          conf.ExtraStateFileAbsPath,
			enableCPUBurst:                 conf.CPUQRMPluginConfig.EnableCPUBurst,
			enableSyncingCPUIdle:           conf.CPUQRMPluginConfig.EnableSyncingCPUIdle,
			enableCPUIdle:                  conf.CPUQRMPluginConfig.EnableCPUIdle,
			enableContainerCPUIdle:         conf.CPUQRMPluginConfig.EnableContainerCPUIdle,
			reclaimRelativeRootCgroupPaths: reclaim.AggregateCgroupPaths(),
			numaBindingReclaimRelativeRootCgroupPaths: reclaim.AggregateNumaBindingCgroupPaths(),
			podDebugAnnoKeys:                conf.PodDebugAnnoKeys,
			podAnnotationKeptKeys:           conf.PodAnnotationKeptKeys,
			podLabelKeptKeys:                conf.PodLabelKeptKeys,
			numaBindingResultAnnotationKey:  conf.NUMABindingResultAnnotationKey,
			numaNumberAnnotationKey:         conf.NUMANumberAnnotationKey,
			numaIDsAnnotationKey:            conf.NUMAIDsAnnotationKey,
			topologyAllocationAnnotationKey: conf.TopologyAllocationAnnotationKey,
			transitionPeriod:                rampUpTransitionPeriod,
		},
		bulkhead: bulkheadComponent{bulkheadManager: bulkheadManager},
	}
	policyImplement.advisorPostCommitCheckpointDir, _ = conf.StateDirectoryConfiguration.GetCurrentAndPreviousStateFileDirectory()
	policyImplement.steadyFakeNUMAMigration = &steadyFakeNUMAMigrationIntentStore{
		dir: func() string { return policyImplement.advisorPostCommitCheckpointDir },
		topology: func() *machine.CPUTopology {
			if policyImplement.machine.machineInfo == nil {
				return nil
			}
			return policyImplement.machine.machineInfo.CPUTopology
		},
	}
	if err := policyImplement.steadyFakeNUMAMigration.restore(); err != nil {
		return false, nil, fmt.Errorf("restore steady fake-NUMA migration target: %w", err)
	}
	if err := policyImplement.restoreAdvisorPostCommitTarget(); err != nil {
		return false, nil, fmt.Errorf("restore pending advisor post-commit target: %w", err)
	}
	policyImplement.installCPUStateWritePermit()

	policyImplement.RegisterAllocationHook(policyImplement.topologyAllocationHook)

	// initialize hint optimizer
	err = policyImplement.initHintOptimizers()
	if err != nil {
		return false, nil, err
	}

	if conf.EnableIRQTuner {
		irqTuner, err := irqtuingcontroller.NewIrqTuningController(conf.AgentConfiguration, policyImplement, policyImplement.emitter.emitter, policyImplement.machine.machineInfo)
		if err != nil {
			general.Errorf("failed to NewIrqTuningController, err %s", err)
			return false, agent.ComponentStub{}, err
		} else {
			policyImplement.irqTuner = irqTuner
		}
	}

	// register allocation behaviors for pods with different QoS level
	policyImplement.allocation.allocationHandlers = map[string]util.AllocationHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresAllocationHandler,
		consts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresAllocationHandler,
	}

	// register hint providers for pods with different QoS level
	policyImplement.allocation.hintHandlers = map[string]util.HintHandler{
		consts.PodAnnotationQoSLevelSharedCores:    policyImplement.sharedCoresHintHandler,
		consts.PodAnnotationQoSLevelDedicatedCores: policyImplement.dedicatedCoresHintHandler,
		consts.PodAnnotationQoSLevelReclaimedCores: policyImplement.reclaimedCoresHintHandler,
		consts.PodAnnotationQoSLevelSystemCores:    policyImplement.systemCoresHintHandler,
	}

	if !policyImplement.hasAnyPendingAdvisorPostCommitTarget() {
		if err := policyImplement.cleanPools(); err != nil {
			return false, agent.ComponentStub{}, fmt.Errorf("cleanPools failed with error: %v", err)
		}

		if err := policyImplement.initReservePool(); err != nil {
			return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy initReservePool failed with error: %v", err)
		}

		if err := policyImplement.initReclaimPool(); err != nil {
			return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy initReclaimPool failed with error: %v", err)
		}
	}

	if err := policyImplement.RegisterCPUSetAdjustmentHandler("bulkhead", policyImplement.bulkhead.bulkheadManager.RunCPUSetAdjustmentHandlers); err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy register bulkhead cpuset adjustment handler failed with error: %v", err)
	}

	if conf.EnableIRQTuner && !policyImplement.hasAnyPendingAdvisorPostCommitTarget() {
		if err := policyImplement.initInterruptPool(); err != nil {
			return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy initInterruptPool failed with error: %v", err)
		}
	}

	state.SetReadonlyState(stateImpl)
	state.SetReadWriteState(stateImpl)

	err = agentCtx.MetaServer.ConfigurationManager.AddConfigWatcher(crd.AdminQoSConfigurationGVR)
	if err != nil {
		return false, nil, err
	}

	if err := agentCtx.MetaServer.ConfigurationManager.AddConfigWatcher(crd.StrategyGroupGVR); err != nil {
		return false, nil, err
	}

	err = agentCtx.ConfigurationManager.AddConfigWatcher(crd.IRQTuningConfigurationGVR)
	if err != nil {
		return false, nil, err
	}

	pluginWrapper, err := skeleton.NewRegistrationPluginWrapper(policyImplement, conf.QRMPluginSocketDirs, func(key string, value int64) {
		_ = wrappedEmitter.StoreInt64(key, value, metrics.MetricTypeNameRaw)
	})
	if err != nil {
		return false, agent.ComponentStub{}, fmt.Errorf("dynamic policy new plugin wrapper failed with error: %v", err)
	}

	return true, &agent.PluginWrapper{GenericPlugin: pluginWrapper}, nil
}

// topologyAllocationHook is an AllocationHook that intercepts allocation info changes and updates topology annotations.
// It generates and merges topology-aware CPU allocation annotations into the allocationInfo when there are physical
// CPU allocation changes, quantity changes, or NUMA-aware topology assignments changes.
func (p *DynamicPolicy) topologyAllocationHook(oldInfo, newInfo *state.AllocationInfo) error {
	if newInfo == nil || !newInfo.CheckMainContainer() || !newInfo.CheckNUMABinding() {
		return nil
	}

	if len(newInfo.TopologyAwareAssignments) == 0 {
		return nil
	}

	if !cpuutil.IsTopologyAllocationChanged(oldInfo, newInfo) {
		return nil
	}

	annotations, err := cpuutil.GetCPUTopologyAllocationsAnnotations(newInfo, p.config.topologyAllocationAnnotationKey)
	if err != nil {
		return err
	}

	newInfo.Annotations = general.MergeAnnotations(newInfo.Annotations, annotations)
	return nil
}

func (p *DynamicPolicy) Name() string {
	return p.name
}

func (p *DynamicPolicy) ResourceName() string {
	return string(v1.ResourceCPU)
}

func (p *DynamicPolicy) startKubeletPodCacheSyncDrivenCPUSetRetry() {
	if p.meta.metaServer == nil || p.meta.metaServer.MetaAgent == nil {
		return
	}
	registrar, ok := p.meta.metaServer.PodFetcher.(podmeta.KubeletPodCacheSyncEventRegistrar)
	if !ok {
		return
	}
	events, unregister := registrar.RegisterKubeletPodCacheSyncListener("dynamic-policy-cpuset-adjustment")
	if events == nil {
		unregister()
		return
	}
	stopCh := p.stopCh
	go func(stop <-chan struct{}) {
		defer unregister()
		for {
			select {
			case event, ok := <-events:
				if !ok {
					return
				}
				if event.CgroupCreated {
					p.handleCgroupCreateEvent()
				}
			case <-stop:
				return
			}
		}
	}(stopCh)
}

func (p *DynamicPolicy) Start() (err error) {
	general.Infof("called")

	p.Lock()
	if p.started {
		general.Infof("is already started")
		p.Unlock()
		return nil
	}
	p.started = true
	p.stopCh = make(chan struct{})
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryStopCh = p.stopCh
	p.adjustment.cpuSetAdjustmentRetryStopping = false
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	p.Unlock()

	defer func() {
		if err != nil {
			p.Lock()
			if p.started {
				p.started = false
				close(p.stopCh)
			}
			p.Unlock()
		}
	}()

	// Defensive restore/startup boundary: a restored checkpoint may carry a
	// reclaimed_cores container in ramp-up (e.g. written by an older version or
	// an async candidate that bypassed precommit). Start() can return an error,
	// so fail closed here rather than silently serving an illegal state. The
	// checkpoint schema is unchanged; this only validates the loaded entries.
	if err := validateNoReclaimedRampUp(p.state.GetPodEntries()); err != nil {
		return fmt.Errorf("startup rejected illegal reclaimed ramp-up in restored state: %w", err)
	}

	if err = p.prepareAdvisorPostCommitTargetOnStart(); err != nil {
		return fmt.Errorf("prepare pending advisor post-commit target: %w", err)
	}
	if p.hasAnyPendingAdvisorPostCommitTarget() {
		p.scheduleCPUSetAdjustmentRetry(cpusetutil.RetryReasonApplyFailed)
	}

	if p.irqTuner != nil {
		go p.irqTuner.Run(p.stopCh)
	}
	p.startKubeletPodCacheSyncDrivenCPUSetRetry()

	go wait.Until(func() {
		_ = p.emitter.emitter.StoreInt64(util.MetricNameHeartBeat, 1, metrics.MetricTypeNameRaw)
		p.emitRuntimeConfigMetrics()
	}, time.Second*30, p.stopCh)

	err = p.registerPeriodicalHandler(cpuconsts.ClearResidualState, general.HealthzCheckStateNotReady,
		qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.clearResidualState, stateCheckPeriod, healthCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed,err:%v", cpuconsts.ClearResidualState, err)
	}

	err = p.registerPeriodicalHandler(cpuconsts.CheckCPUSet, general.HealthzCheckStateNotReady,
		qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.checkCPUSet, cpusetCheckPeriod, healthCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed,err:%v", cpuconsts.CheckCPUSet, err)
	}

	err = p.registerPeriodicalHandler(cpuconsts.SyncSystemExclusivePool, general.HealthzCheckStateNotReady,
		qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.syncSystemExclusivePool, syncSystemExclusivePoolPeriod, healthCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed,err:%v", cpuconsts.SyncSystemExclusivePool, err)
	}

	err = p.registerPeriodicalHandler(cpuconsts.SyncBulkhead, general.HealthzCheckStateNotReady,
		qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.runBulkheadPeriodicalHandlers, syncBulkheadPeriod, healthCheckTolerationTimes)
	if err != nil {
		general.Errorf("start %v failed,err:%v", cpuconsts.SyncBulkhead, err)
	}

	// start cpu-idle syncing if needed
	if p.config.enableSyncingCPUIdle {
		general.Infof("syncCPUIdle enabled")

		if len(p.config.reclaimRelativeRootCgroupPaths) == 0 {
			return fmt.Errorf("enable syncing cpu idle but not set reclaiemd relative root cgroup path in configuration")
		}

		err = p.registerPeriodicalHandler(cpuconsts.SyncCPUIdle, general.HealthzCheckStateNotReady,
			qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.syncCPUIdle, syncCPUIdlePeriod, healthCheckTolerationTimes)
		if err != nil {
			general.Errorf("start %v failed,err:%v", cpuconsts.SyncCPUIdle, err)
		}
	}

	if p.config.enableContainerCPUIdle {
		general.Infof("container cpu idle is enabled")

		err = p.registerPeriodicalHandler(cpuconsts.SyncContainerCPUIdle, general.HealthzCheckStateNotReady,
			qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.syncContainerCPUIdle, syncContainerCPUIdlePeriod, healthCheckTolerationTimes)
		if err != nil {
			general.Errorf("start %v failed,err:%v", cpuconsts.SyncContainerCPUIdle, err)
			return err
		}
	}

	// start cpu burst sync if needed
	if p.config.enableCPUBurst {
		general.Infof("cpu burst is enabled")

		err = p.registerPeriodicalHandler(cpuconsts.SyncCPUBurst, general.HealthzCheckStateNotReady,
			qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.syncCPUBurst, syncCPUBurstPeriod, healthCheckTolerationTimes)
		if err != nil {
			general.Errorf("start %v failed,err:%v", cpuconsts.SyncCPUBurst, err)
		}
	}

	if p.config.conf.CPUQRMPluginConfig.EnableCPUWeight {
		general.Infof("cpu weight is enabled")

		err = p.registerPeriodicalHandler(cpuconsts.SyncCPUWeight, general.HealthzCheckStateNotReady,
			qrm.QRMCPUPluginPeriodicalHandlerGroupName, p.syncCPUWeight, syncCPUWeightPeriod, healthCheckTolerationTimes)
		if err != nil {
			general.Errorf("start %v failed,err:%v", cpuconsts.SyncCPUWeight, err)
		}
	}

	// start cpu-pressure eviction plugin if needed
	if p.cpuPressureEviction != nil {
		p.Lock()
		if p.started {
			var ctx context.Context
			ctx, p.cpuPressureEvictionCancel = context.WithCancel(context.Background())
			go p.cpuPressureEviction.Run(ctx)
		}
		p.Unlock()
	}

	go wait.Until(func() {
		periodicalhandler.ReadyToStartHandlersByGroup(qrm.QRMCPUPluginPeriodicalHandlerGroupName)
	}, 5*time.Second, p.stopCh)

	// pre-check necessary dirs if sys-advisor is enabled
	if !p.config.enableCPUAdvisor {
		general.Infof("start dynamic policy cpu plugin without sys-advisor")
		return nil
	} else if p.config.cpuAdvisorSocketAbsPath == "" || p.config.cpuPluginSocketAbsPath == "" {
		return fmt.Errorf("invalid cpuAdvisorSocketAbsPath: %s or cpuPluginSocketAbsPath: %s",
			p.config.cpuAdvisorSocketAbsPath, p.config.cpuPluginSocketAbsPath)
	}

	general.Infof("start dynamic policy cpu plugin with sys-advisor")
	general.RegisterHeartbeatCheck(cpuconsts.CommunicateWithAdvisor, 2*time.Minute, general.HealthzCheckStateNotReady, 2*time.Minute)

	err = p.initAdvisorClientConn()
	if err != nil {
		general.Errorf("initAdvisorClientConn failed with error: %v", err)
		return
	}

	p.advisor.advisorMonitor, err = timemonitor.NewTimeMonitor(cpuAdvisorHealthMonitorName, cpuAdvisorHealthMonitorInterval,
		cpuAdvisorUnhealthyThreshold, cpuAdvisorHealthyThreshold,
		util.MetricNameAdvisorUnhealthy, p.emitter.emitter, cpuAdvisorHealthyCount, true)
	if err != nil {
		general.Errorf("initialize cpu advisor monitor failed with error: %v", err)
		return
	}
	go p.advisor.advisorMonitor.Run(p.stopCh)

	go wait.BackoffUntil(func() { p.serveForAdvisor(p.stopCh) }, wait.NewExponentialBackoffManager(
		800*time.Millisecond, 30*time.Second, 2*time.Minute, 2.0, 0, &clock.RealClock{}), true, p.stopCh)

	communicateWithCPUAdvisorServer := func() {
		general.Infof("waiting cpu plugin checkpoint server serving confirmation")
		if conn, err := process.Dial(p.config.cpuPluginSocketAbsPath, 5*time.Second); err != nil {
			general.Errorf("dial check at socket: %s failed with err: %v", p.config.cpuPluginSocketAbsPath, err)
			return
		} else {
			_ = conn.Close()
		}
		general.Infof("cpu plugin checkpoint server serving confirmed")

		p.getAdviceFromAdvisorLoop(p.stopCh)
		select {
		case <-p.stopCh:
			// stopCh closed, no need to fall back to ListAndWatch.
			return
		default:
		}

		general.Infof("advisor does not implement GetAdvice, fall back to ListAndWatch")

		if err := p.pushCPUAdvisor(); err != nil {
			general.Errorf("sync existing containers to cpu advisor failed with error: %v", err)
			return
		}
		general.Infof("sync existing containers to cpu advisor successfully")

		// call lw of CPUAdvisorServer and do allocation
		if err := p.lwCPUAdvisorServer(p.stopCh); err != nil {
			general.Errorf("lwCPUAdvisorServer failed with error: %v", err)
		} else {
			general.Infof("lwCPUAdvisorServer finished")
		}
	}

	go wait.BackoffUntil(communicateWithCPUAdvisorServer, wait.NewExponentialBackoffManager(800*time.Millisecond,
		30*time.Second, 2*time.Minute, 2.0, 0, &clock.RealClock{}), true, p.stopCh)

	err = p.resourcePackageManager.Run(p.stopCh)
	if err != nil {
		return fmt.Errorf("resourcePackageManager.Run failed with error: %v", err)
	}

	p.syncResourcePackagePinnedCPUSet()
	go wait.Until(p.syncResourcePackagePinnedCPUSet, 30*time.Second, p.stopCh)

	err = p.hintOpt.sharedCoresNUMABindingHintOptimizer.Run(p.stopCh)
	if err != nil {
		return fmt.Errorf("sharedCoresNUMABindingHintOptimizer.Run failed with error: %v", err)
	}

	err = p.hintOpt.dedicatedCoresNUMABindingHintOptimizer.Run(p.stopCh)
	if err != nil {
		return fmt.Errorf("dedicatedCoresNUMABindingHintOptimizer.Run failed with error: %v", err)
	}

	return nil
}

func (p *DynamicPolicy) emitRuntimeConfigMetrics() {
	reclaimEnabled := int64(0)
	if p.isReclaimEnabled() {
		reclaimEnabled = 1
	}
	_ = p.emitter.emitter.StoreInt64(util.MetricNameReclaimEnabled, reclaimEnabled, metrics.MetricTypeNameRaw)
}

func (p *DynamicPolicy) Stop() error {
	p.Lock()
	if !p.started {
		general.Warningf("already stopped")
		p.Unlock()
		return nil
	}

	p.started = false
	stopCh := p.stopCh
	p.adjustment.cpuSetAdjustmentRetryMu.Lock()
	p.adjustment.cpuSetAdjustmentRetryStopping = true
	p.adjustment.cpuSetAdjustmentRetryMu.Unlock()
	close(stopCh)
	p.Unlock()
	p.adjustment.cpuSetAdjustmentRetryWG.Wait()

	if p.cpuPressureEvictionCancel != nil {
		p.cpuPressureEvictionCancel()
	}

	periodicalhandler.StopHandlersByGroup(qrm.QRMCPUPluginPeriodicalHandlerGroupName)

	if p.advisor.advisorConn != nil {
		if err := p.advisor.advisorConn.Close(); err != nil {
			return err
		}
	}

	general.Infof("stopped")
	return nil
}

// GetResourcesAllocation returns allocation results of corresponding resources
func (p *DynamicPolicy) GetResourcesAllocation(ctx context.Context,
	req *pluginapi.GetResourcesAllocationRequest,
) (*pluginapi.GetResourcesAllocationResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetResourcesAllocation got nil req")
	}

	general.Infof("called")
	p.Lock()
	defer p.Unlock()
	releaseExecution, err := p.acquireCPUSetAdjustmentExecutionLeaseLocked(
		ctx, "GetResourcesAllocation")
	if err != nil {
		return nil, err
	}
	defer releaseExecution.release()
	ctx = context.WithValue(ctx, cpuSetAdjustmentExecutionLeaseContextKey{}, releaseExecution)

	expectedRevision := p.state.GetRevision()
	podEntries := p.state.GetPodEntries()
	machineState := p.state.GetMachineState()
	hasSharedRampUpExit := false
	now := time.Now()
	for _, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		main := containerEntries.GetMainContainerEntry()
		finishRampUp, _ := shouldAllocationFinishRampUp(main, p.config.transitionPeriod, now)
		if finishRampUp && main.CheckShared() {
			hasSharedRampUpExit = true
			break
		}
	}

	// rumpUpPooledCPUs is the total available cpu cores minus those that are reserved
	rumpUpPooledCPUs := machineState.GetFilteredAvailableCPUSet(p.config.reservedCPUs,
		func(ai *state.AllocationInfo) bool {
			return ai.CheckDedicated() || ai.CheckSharedNUMABinding()
		},
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicatedNUMABinding))
	var allocationInfosJustFinishRampUp []*state.AllocationInfo
	needUpdateMachineState := false
	for podUID, containerEntries := range podEntries {
		// if it's a pool, not returning to QRM
		if containerEntries.IsPoolEntry() {
			continue
		}

		mainContainerAllocationInfo := podEntries[podUID].GetMainContainerEntry()
		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				continue
			}
			originAllocationInfo := allocationInfo
			allocationInfo = allocationInfo.Clone()

			// sync allocation info from main container to sidecar
			if allocationInfo.CheckSideCar() && mainContainerAllocationInfo != nil {
				if hasSharedRampUpExit {
					// The precommit phase synchronizes sidecars after their main
					// containers and pools have reached the final candidate.
					continue
				}
				if p.applySidecarAllocationInfoFromMainContainer(allocationInfo, mainContainerAllocationInfo) {
					general.Infof("pod: %s/%s, container: %s sync allocation info from main container",
						allocationInfo.PodNamespace, allocationInfo.PodName, containerName)
					if err := p.updateAllocationInfo(podUID, containerName, originAllocationInfo, allocationInfo, true); err != nil {
						general.Errorf("updateAllocationInfo failed for pod: %s/%s, container: %s: %v",
							allocationInfo.PodNamespace, allocationInfo.PodName, containerName, err)
						continue
					}
					needUpdateMachineState = true
				}
				// A sidecar mirrors its main container and must not advance the
				// ramp-up lifecycle independently using its own timestamp.
				continue
			}

			_, tsErr := time.Parse(util.QRMTimeFormat, allocationInfo.InitTimestamp)
			if tsErr != nil {
				if allocationInfo.CheckShared() && !allocationInfo.CheckNUMABinding() {
					general.Errorf("pod: %s/%s, container: %s init timestamp parsed failed with error: %v, re-ramp-up it",
						allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName, tsErr)

					// Legacy timestamp repair re-ramps this non-binding shared
					// allocation, so it activates the global reclaim domain; the
					// derive function unions it with the domains already recorded
					// in the state entries.
					enteringRampUpDomains, dErr := p.rampUpDomainForAllocation(allocationInfo)
					if dErr != nil {
						return nil, fmt.Errorf("resolve entering legacy re-ramp-up domains: %w", dErr)
					}
					rampUpReclaimFloor, err := p.deriveRampUpReclaimFloor(
						machineState, p.state.GetPodEntries(), enteringRampUpDomains)
					if err != nil {
						return nil, fmt.Errorf("derive reclaim floor for legacy re-ramp-up failed: %w", err)
					}
					clonedPooledCPUs := rumpUpPooledCPUs.Difference(rampUpReclaimFloor)
					clonedPooledCPUsTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(
						p.machine.machineInfo.CPUTopology, clonedPooledCPUs)
					if err != nil {
						return nil, fmt.Errorf("get NUMA assignments for legacy re-ramp-up failed: %w", err)
					}

					allocationInfo.AllocationResult = clonedPooledCPUs
					allocationInfo.OriginalAllocationResult = clonedPooledCPUs
					allocationInfo.TopologyAwareAssignments = clonedPooledCPUsTopologyAwareAssignments
					allocationInfo.OriginalTopologyAwareAssignments = clonedPooledCPUsTopologyAwareAssignments
					// fill OwnerPoolName with empty string when ramping up
					allocationInfo.OwnerPoolName = commonstate.EmptyOwnerPoolName
					allocationInfo.RampUp = true
				}

				allocationInfo.InitTimestamp = time.Now().Format(util.QRMTimeFormat)
				if hasSharedRampUpExit {
					podEntries[podUID][containerName] = allocationInfo
					needUpdateMachineState = true
					continue
				}
				if err := p.updateAllocationInfo(podUID, containerName, originAllocationInfo, allocationInfo, true); err != nil {
					general.Errorf("updateAllocationInfo failed for pod: %s/%s, container: %s: %v",
						allocationInfo.PodNamespace, allocationInfo.PodName, containerName, err)
				}
			} else if finishRampUp, _ := shouldAllocationFinishRampUp(allocationInfo, p.config.transitionPeriod, time.Now()); finishRampUp {
				general.Infof("pod: %s/%s, container: %s ramp up finished", allocationInfo.PodNamespace, allocationInfo.PodName, allocationInfo.ContainerName)
				allocationInfo.RampUp = false
				if allocationInfo.CheckShared() {
					podEntries[podUID][containerName] = allocationInfo
					allocationInfosJustFinishRampUp = append(allocationInfosJustFinishRampUp, allocationInfo)
					continue
				}
				if hasSharedRampUpExit {
					podEntries[podUID][containerName] = allocationInfo
					needUpdateMachineState = true
					continue
				}
				if err := p.updateAllocationInfo(podUID, containerName, originAllocationInfo, allocationInfo, true); err != nil {
					general.Errorf("updateAllocationInfo failed for pod: %s/%s, container: %s: %v",
						allocationInfo.PodNamespace, allocationInfo.PodName, containerName, err)
					continue
				}
			}

		}
	}

	if len(allocationInfosJustFinishRampUp) > 0 {
		if err := p.putAllocationsAndAdjustAllocationEntriesAtRevision(
			ctx, podEntries, allocationInfosJustFinishRampUp, true, true, expectedRevision); err != nil {
			// not influencing return response to kubelet when putAllocationsAndAdjustAllocationEntries failed
			general.Errorf("putAllocationsAndAdjustAllocationEntries failed with error: %v", err)
		}
	} else if needUpdateMachineState {
		// NOTE: we only need update machine state when putAllocationsAndAdjustAllocationEntries is skipped,
		// because putAllocationsAndAdjustAllocationEntries will update machine state.
		general.Infof("GetResourcesAllocation update machine state")
		podEntries = p.state.GetPodEntries()
		updatedMachineState, err := generateMachineStateFromPodEntries(p.machine.machineInfo.CPUTopology, podEntries, machineState)
		if err != nil {
			general.Errorf("GetResourcesAllocation GenerateMachineStateFromPodEntries failed with error: %v", err)
			return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
		}
		if err := p.state.SetMachineState(updatedMachineState, true); err != nil {
			return nil, fmt.Errorf("persist resources allocation machine state: %w", err)
		}
	}

	podEntries = p.state.GetPodEntries()
	podResources := make(map[string]*pluginapi.ContainerResources)
	for podUID, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}

		if podResources[podUID] == nil {
			podResources[podUID] = &pluginapi.ContainerResources{}
		}

		for containerName, allocationInfo := range containerEntries {
			if allocationInfo == nil {
				general.Warningf("container %s allocation info is nil during GetResourcesAllocation, skip it", containerName)
				continue
			}
			if podResources[podUID].ContainerResources == nil {
				podResources[podUID].ContainerResources = make(map[string]*pluginapi.ResourceAllocation)
			}

			topologyAssignments := make(map[uint64]uint64)
			for numaID, cset := range allocationInfo.TopologyAwareAssignments {
				topologyAssignments[uint64(numaID)] = uint64(cset.Size())
			}

			podResources[podUID].ContainerResources[containerName] = &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						OciPropertyName:     util.OCIPropertyNameCPUSetCPUs,
						IsNodeResource:      false,
						IsScalarResource:    true,
						AllocatedQuantity:   float64(allocationInfo.AllocationResult.Size()),
						AllocationResult:    allocationInfo.AllocationResult.String(),
						TopologyAssignments: topologyAssignments,
						Annotations:         general.DeepCopyMap(allocationInfo.Annotations),
					},
				},
			}
			if p.shouldBypassCPUSetAdjustmentForAllocation(allocationInfo) {
				clearCPUSetInAllocation(podResources[podUID].ContainerResources[containerName])
			}
		}
	}

	return &pluginapi.GetResourcesAllocationResponse{
		PodResources: podResources,
	}, nil
}

// GetTopologyAwareResources returns allocation results of corresponding resources as machineInfo aware format
func (p *DynamicPolicy) GetTopologyAwareResources(_ context.Context,
	req *pluginapi.GetTopologyAwareResourcesRequest,
) (*pluginapi.GetTopologyAwareResourcesResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyAwareResources got nil req")
	}

	general.Infof("called")
	p.RLock()
	defer p.RUnlock()

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	if allocationInfo == nil {
		return nil, fmt.Errorf("pod: %s, container: %s is not show up in cpu plugin state", req.PodUid, req.ContainerName)
	}

	resp := &pluginapi.GetTopologyAwareResourcesResponse{
		PodUid:       allocationInfo.PodUid,
		PodName:      allocationInfo.PodName,
		PodNamespace: allocationInfo.PodNamespace,
		ContainerTopologyAwareResources: &pluginapi.ContainerTopologyAwareResources{
			ContainerName: allocationInfo.ContainerName,
		},
	}

	if allocationInfo.CheckSideCar() {
		resp.ContainerTopologyAwareResources.AllocatedResources = map[string]*pluginapi.TopologyAwareResource{
			string(v1.ResourceCPU): {
				IsNodeResource:                    false,
				IsScalarResource:                  true,
				AggregatedQuantity:                0,
				OriginalAggregatedQuantity:        0,
				TopologyAwareQuantityList:         nil,
				OriginalTopologyAwareQuantityList: nil,
			},
		}
	} else {
		resp.ContainerTopologyAwareResources.AllocatedResources = map[string]*pluginapi.TopologyAwareResource{
			string(v1.ResourceCPU): {
				IsNodeResource:                    false,
				IsScalarResource:                  true,
				AggregatedQuantity:                float64(allocationInfo.AllocationResult.Size()),
				OriginalAggregatedQuantity:        float64(allocationInfo.OriginalAllocationResult.Size()),
				TopologyAwareQuantityList:         util.GetTopologyAwareQuantityFromAssignments(allocationInfo.TopologyAwareAssignments),
				OriginalTopologyAwareQuantityList: util.GetTopologyAwareQuantityFromAssignments(allocationInfo.OriginalTopologyAwareAssignments),
			},
		}
	}

	return resp, nil
}

// GetTopologyAwareAllocatableResources returns corresponding allocatable resources as machineInfo aware format
func (p *DynamicPolicy) GetTopologyAwareAllocatableResources(_ context.Context,
	_ *pluginapi.GetTopologyAwareAllocatableResourcesRequest,
) (*pluginapi.GetTopologyAwareAllocatableResourcesResponse, error) {
	general.Infof("is called")

	numaNodes := p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt()
	topologyAwareAllocatableQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(numaNodes))
	topologyAwareCapacityQuantityList := make([]*pluginapi.TopologyAwareQuantity, 0, len(numaNodes))

	for _, numaNode := range numaNodes {
		numaNodeCPUs := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaNode).Clone()
		topologyAwareAllocatableQuantityList = append(topologyAwareAllocatableQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(numaNodeCPUs.Difference(p.config.reservedCPUs).Size()),
			Node:          uint64(numaNode),
		})
		topologyAwareCapacityQuantityList = append(topologyAwareCapacityQuantityList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: float64(numaNodeCPUs.Size()),
			Node:          uint64(numaNode),
		})
	}

	allocatableResources := map[string]*pluginapi.AllocatableTopologyAwareResource{
		string(v1.ResourceCPU): {
			IsNodeResource:                       false,
			IsScalarResource:                     true,
			AggregatedAllocatableQuantity:        float64(p.machine.machineInfo.NumCPUs - p.config.reservedCPUs.Size()),
			TopologyAwareAllocatableQuantityList: topologyAwareAllocatableQuantityList,
			AggregatedCapacityQuantity:           float64(p.machine.machineInfo.NumCPUs),
			TopologyAwareCapacityQuantityList:    topologyAwareCapacityQuantityList,
		},
	}

	p.addReclaimedCPUAllocatable(allocatableResources, numaNodes, p.reclaim.reclaimConsumersForKCNR)

	return &pluginapi.GetTopologyAwareAllocatableResourcesResponse{
		AllocatableResources: allocatableResources,
	}, nil
}

// addReclaimedCPUAllocatable inserts a ReclaimedResourceMilliCPU entry
// into allocatableResources when ReclaimConsumersForKCNR is configured.
// The reported value is (headroom_cores * 1000 * summed_percentage / 100),
// converting cores to millicpu; summed_percentage is the sum of
// GetReclaimedPercentage across every configured consumer name, capped at
// 100 (unknown names contribute 0 and are logged by GetReclaimedPercentage).
func (p *DynamicPolicy) addReclaimedCPUAllocatable(
	allocatableResources map[string]*pluginapi.AllocatableTopologyAwareResource,
	numaNodes []int,
	consumerNames []string,
) {
	if !p.config.conf.EnableReclaimedResourceAllocatableReporting {
		return
	}

	if len(consumerNames) == 0 {
		return
	}

	numaHeadroom := p.state.GetNUMAHeadroom()
	reclaimedNUMAHeadroom := reclaim.GetReclaimedNUMAHeadroom(
		numaHeadroom, p.config.dynamicConfig.GetDynamicConfiguration(), consumerNames...)

	topologyAwareList := make([]*pluginapi.TopologyAwareQuantity, 0, len(numaNodes))
	for _, numaNode := range numaNodes {
		topologyAwareList = append(topologyAwareList, &pluginapi.TopologyAwareQuantity{
			ResourceValue: reclaimedNUMAHeadroom[numaNode] * 1000,
			Node:          uint64(numaNode),
		})
	}

	var totalReclaimedHeadroom float64
	for _, v := range reclaimedNUMAHeadroom {
		totalReclaimedHeadroom += v
	}
	aggregated := totalReclaimedHeadroom * 1000

	allocatableResources[string(consts.ReclaimedResourceMilliCPU)] = &pluginapi.AllocatableTopologyAwareResource{
		IsNodeResource:                       false,
		IsScalarResource:                     true,
		AggregatedAllocatableQuantity:        aggregated,
		TopologyAwareAllocatableQuantityList: topologyAwareList,
		AggregatedCapacityQuantity:           aggregated,
		TopologyAwareCapacityQuantityList:    topologyAwareList,
	}
}

// GetTopologyHints returns hints of corresponding resources
func (p *DynamicPolicy) GetTopologyHints(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceHintsResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("GetTopologyHints got nil req")
	}

	// identify if the pod is a debug pod,
	// if so, apply specific strategy to it.
	// since GetKatalystQoSLevelFromResourceReq function will filter annotations,
	// we should do it before GetKatalystQoSLevelFromResourceReq.
	isDebugPod := util.IsDebugPod(req.Annotations, p.config.podDebugAnnoKeys)

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.config.qosConfig, req, p.config.podAnnotationKeptKeys, p.config.podLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	reqInt, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"podType", req.PodType,
		"podRole", req.PodRole,
		"containerType", req.ContainerType,
		"qosLevel", qosLevel,
		"numCPUsInt", reqInt,
		"numCPUsFloat64", reqFloat64,
		"isDebugPod", isDebugPod,
		"annotation", req.Annotations)

	if req.ContainerType == pluginapi.ContainerType_INIT || isDebugPod {
		general.Infof("there is no NUMA preference, return nil hint")
		return util.PackResourceHintsResponse(req, string(v1.ResourceCPU),
			map[string]*pluginapi.ListOfTopologyHints{
				string(v1.ResourceCPU): nil, // indicates that there is no numa preference
			})
	}

	startTime := time.Now()
	p.RLock()
	defer func() {
		p.RUnlock()
		if err != nil {
			inplaceUpdateResizing := util.PodInplaceUpdateResizing(req)
			_ = p.emitter.emitter.StoreInt64(util.MetricNameGetTopologyHintsFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)},
				metrics.MetricTag{Key: util.MetricTagNameInplaceUpdateResizing, Val: strconv.FormatBool(inplaceUpdateResizing)})

			general.ErrorS(err, "GetTopologyHints failed",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
				"inplaceUpdateResizing", inplaceUpdateResizing,
			)
		}
		general.InfoS("finished",
			"duration", time.Since(startTime).String(),
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
		)
	}()

	if p.allocation.hintHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}
	return p.allocation.hintHandlers[qosLevel](ctx, req)
}

// GetPodTopologyHints returns hints of corresponding resources for pod
func (p *DynamicPolicy) GetPodTopologyHints(ctx context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceHintsResponse, err error) {
	return nil, util.ErrNotImplemented
}

// GetResourcePluginOptions returns options to be communicated with Resource Manager
func (p *DynamicPolicy) GetResourcePluginOptions(context.Context,
	*pluginapi.Empty,
) (*pluginapi.ResourcePluginOptions, error) {
	general.Infof("called")
	return &pluginapi.ResourcePluginOptions{
		PreStartRequired:      false,
		WithTopologyAlignment: true,
		NeedReconcile:         true,
	}, nil
}

// acquireCPUSetAdjustmentExecutionLocked serializes state mutation with a
// complete cpuset adjustment round. The caller must hold p.Lock; this method
// always returns with p.Lock held.
func (p *DynamicPolicy) acquireCPUSetAdjustmentExecutionLocked(
	ctx context.Context,
) (*cpuSetAdjustmentExecutionLease, error) {
	if p.adjustment.cpuSetAdjustmentExecution == nil {
		p.adjustment.cpuSetAdjustmentExecution = make(chan struct{}, 1)
	}
	execution := p.adjustment.cpuSetAdjustmentExecution
	p.Unlock()
	select {
	case execution <- struct{}{}:
	case <-ctx.Done():
		p.Lock()
		return nil, ctx.Err()
	}
	p.Lock()
	if err := ctx.Err(); err != nil {
		<-execution
		return nil, err
	}
	return &cpuSetAdjustmentExecutionLease{
		policy:    p,
		execution: execution,
		active:    true,
	}, nil
}

// acquireCPUSetAdjustmentExecutionLeaseLocked waits for the advisor/retry
// owner, then holds the adjustment execution lease while the caller mutates
// state. Rechecking the target after acquiring the lease closes the race with
// a target published while the policy lock was released.
func (p *DynamicPolicy) acquireCPUSetAdjustmentExecutionLeaseLocked(
	ctx context.Context,
	source string,
) (*cpuSetAdjustmentExecutionLease, error) {
	for {
		if err := p.waitForPendingAdvisorPostCommitTargetLocked(ctx, source); err != nil {
			return nil, err
		}
		releaseExecution, err := p.acquireCPUSetAdjustmentExecutionLocked(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s waiting for cpuset adjustment execution: %w", source, err)
		}
		if target, _ := p.currentAdvisorPostCommitTargetAndChange(); target == nil {
			return releaseExecution, nil
		}
		releaseExecution.release()
	}
}

// waitForPendingAdvisorPostCommitTargetLocked waits for the existing
// advisor/retry owner to clear a pending target. The caller must hold p.Lock;
// this method always returns with p.Lock held.
func (p *DynamicPolicy) waitForPendingAdvisorPostCommitTargetLocked(
	ctx context.Context,
	source string,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%s blocked by pending advisor post-commit target: %w", source, err)
		}
		target, changed := p.currentAdvisorPostCommitTargetAndChange()
		if target == nil {
			return nil
		}

		// Release an aged physical-apply writer fence after replaying every
		// response-owned side effect. The target and WAL remain available for a
		// complete retry, so liveness does not trade away crash consistency.
		if p.advisorPostCommitProgressStuck(p.currentAdvisorPostCommitProgress()) {
			_, err := p.recoverStuckAdvisorPostCommitTargetLocked(ctx, target)
			if err != nil {
				return fmt.Errorf("%s recovering stuck advisor post-commit revision %d: %w",
					source, target.revision, err)
			}
			continue
		}

		// Re-evaluate the stuck condition on a bounded timer in addition to the
		// target-change notification. After the first physical-apply progress
		// record, further driver retries are no-ops and emit no `changed` event,
		// so a waiter that arrived while the target was fresh would otherwise
		// sleep its entire deadline and never notice the target became eligible
		// for the safe writer-fence handoff.
		timer := time.NewTimer(advisorPostCommitWaitReevalInterval)
		p.Unlock()
		select {
		case <-ctx.Done():
		case <-changed:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		p.Lock()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf(
				"%s blocked by pending advisor post-commit revision %d: %w",
				source, target.revision, err)
		}
	}
}

// Allocate is called during pod admit so that the resource
// plugin can allocate corresponding resource for the container
// according to resource request
func (p *DynamicPolicy) Allocate(ctx context.Context,
	req *pluginapi.ResourceRequest,
) (resp *pluginapi.ResourceAllocationResponse, respErr error) {
	if req == nil {
		return nil, fmt.Errorf("allocate got nil req")
	}
	ctx = withAllocationPodMeta(ctx, req)

	// identify if the pod is a debug pod,
	// if so, apply specific strategy to it.
	// since GetKatalystQoSLevelFromResourceReq function will filter annotations,
	// we should do it before GetKatalystQoSLevelFromResourceReq.
	isDebugPod := util.IsDebugPod(req.Annotations, p.config.podDebugAnnoKeys)

	existReallocAnno, isReallocation := util.IsReallocation(req.Annotations)

	qosLevel, err := util.GetKatalystQoSLevelFromResourceReq(p.config.qosConfig, req, p.config.podAnnotationKeptKeys, p.config.podLabelKeptKeys)
	if err != nil {
		err = fmt.Errorf("GetKatalystQoSLevelFromResourceReq for pod: %s/%s, container: %s failed with error: %v",
			req.PodNamespace, req.PodName, req.ContainerName, err)
		general.Errorf("%s", err.Error())
		return nil, err
	}

	reqInt, reqFloat64, err := util.GetQuantityFromResourceReq(req)
	if err != nil {
		return nil, fmt.Errorf("getReqQuantityFromResourceReq failed with error: %v", err)
	}

	general.InfoS("called",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"podType", req.PodType,
		"podRole", req.PodRole,
		"containerType", req.ContainerType,
		"qosLevel", qosLevel,
		"numCPUsInt", reqInt,
		"numCPUsFloat64", reqFloat64,
		"isDebugPod", isDebugPod,
		"annotations", req.Annotations)

	if req.ContainerType == pluginapi.ContainerType_INIT {
		return &pluginapi.ResourceAllocationResponse{
			PodUid:         req.PodUid,
			PodNamespace:   req.PodNamespace,
			PodName:        req.PodName,
			ContainerName:  req.ContainerName,
			ContainerType:  req.ContainerType,
			ContainerIndex: req.ContainerIndex,
			PodRole:        req.PodRole,
			PodType:        req.PodType,
			ResourceName:   string(v1.ResourceCPU),
			Labels:         general.DeepCopyMap(req.Labels),
			Annotations:    general.DeepCopyMap(req.Annotations),
		}, nil
	} else if isDebugPod {
		return &pluginapi.ResourceAllocationResponse{
			PodUid:         req.PodUid,
			PodNamespace:   req.PodNamespace,
			PodName:        req.PodName,
			ContainerName:  req.ContainerName,
			ContainerType:  req.ContainerType,
			ContainerIndex: req.ContainerIndex,
			PodRole:        req.PodRole,
			PodType:        req.PodType,
			ResourceName:   string(v1.ResourceCPU),
			AllocationResult: &pluginapi.ResourceAllocation{
				ResourceAllocation: map[string]*pluginapi.ResourceAllocationInfo{
					string(v1.ResourceCPU): {
						// return ResourceAllocation with empty OciPropertyName, AllocatedQuantity, AllocationResult for containers in debug pod,
						// it won't influence oci spec properties of the container
						IsNodeResource:   false,
						IsScalarResource: true,
					},
				},
			},
			Labels:      general.DeepCopyMap(req.Labels),
			Annotations: general.DeepCopyMap(req.Annotations),
		}, nil
	}

	startTime := time.Now()
	unlockAllocationRequest := p.lockAllocationRequest(req.PodUid, req.ContainerName)
	defer unlockAllocationRequest()
	p.Lock()
	var rollbackSnapshot *allocationRollbackSnapshot
	var executionLease *cpuSetAdjustmentExecutionLease
	defer func() {
		// calls sys-advisor to inform the latest container
		if p.config.enableCPUAdvisor && respErr == nil && req.ContainerType != pluginapi.ContainerType_INIT {
			_, err := p.advisor.advisorClient.AddContainer(ctx, &advisorsvc.ContainerMetadata{
				PodUid:               req.PodUid,
				PodNamespace:         req.PodNamespace,
				PodName:              req.PodName,
				ContainerName:        req.ContainerName,
				ContainerType:        req.ContainerType,
				ContainerIndex:       req.ContainerIndex,
				Labels:               maputil.CopySS(req.Labels),
				Annotations:          maputil.CopySS(req.Annotations),
				QosLevel:             qosLevel,
				RequestQuantity:      uint64(reqInt),
				RequestMilliQuantity: uint64(reqFloat64 * 1000),
				UseMilliQuantity:     true,
			})
			if err != nil {
				resp = nil
				respErr = fmt.Errorf("add container to qos aware server failed with error: %v", err)
			}
		}
		if respErr != nil {
			inplaceUpdateResizing := util.PodInplaceUpdateResizing(req)
			var compensated *requestStateCompensatedError
			if rollbackSnapshot != nil && !errors.As(respErr, &compensated) {
				if err := p.rollbackAllocationState(req, *rollbackSnapshot); err != nil {
					respErr = fmt.Errorf("%w; allocation rollback failed: %v", respErr, err)
				}
			}

			metricTags := []metrics.MetricTag{
				{Key: "error_message", Val: metric.MetricTagValueFormat(respErr)},
				{Key: util.MetricTagNameInplaceUpdateResizing, Val: strconv.FormatBool(inplaceUpdateResizing)},
			}
			if existReallocAnno {
				metricTags = append(metricTags, metrics.MetricTag{Key: "reallocation", Val: isReallocation})
			}
			_ = p.emitter.emitter.StoreInt64(util.MetricNameAllocateFailed, 1, metrics.MetricTypeNameRaw, metricTags...)
		}
		if rollbackSnapshot != nil {
			if err := p.state.StoreState(); err != nil {
				general.ErrorS(err, "store state failed", "podName", req.PodName, "containerName", req.ContainerName)
				resp = nil
				if respErr == nil {
					respErr = fmt.Errorf("store allocation state failed: %w", err)
					if rollbackErr := p.rollbackAllocationState(req, *rollbackSnapshot); rollbackErr != nil {
						respErr = fmt.Errorf("%w; allocation rollback failed: %v", respErr, rollbackErr)
					}
				} else {
					respErr = fmt.Errorf("%w; store allocation state failed: %v", respErr, err)
				}
			}
		}

		executionLease.release()
		p.Unlock()
		if respErr != nil {
			general.ErrorS(respErr, "Allocate failed",
				"podNamespace", req.PodNamespace,
				"podName", req.PodName,
				"containerName", req.ContainerName,
			)
		}
		general.InfoS("finished",
			"duration", time.Since(startTime).String(),
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
		)
		return
	}()

	executionLease, err = p.acquireCPUSetAdjustmentExecutionLeaseLocked(ctx, "Allocate")
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, cpuSetAdjustmentExecutionLeaseContextKey{}, executionLease)
	rollbackSnapshot = &allocationRollbackSnapshot{
		revision:                p.state.GetRevision(),
		podEntries:              p.state.GetPodEntries(),
		machineState:            p.state.GetMachineState(),
		allowOverlap:            p.state.GetAllowSharedCoresOverlapReclaimedCores(),
		disableDedicatedOverlap: p.state.GetDisableDedicatedCoresOverlapReclaimedCores(),
	}

	allocationInfo := p.state.GetAllocationInfo(req.PodUid, req.ContainerName)
	// Defensive: a checkpoint/restored state may carry a reclaimed_cores
	// container in ramp-up. Reject it here so the existing-allocation fast
	// return cannot hand back an illegal allocation, and so the legacy direct
	// Allocate path does not silently continue. Reuse the single authoritative
	// predicate rather than duplicating the condition.
	if allocationInfo != nil {
		if err := validateNoReclaimedRampUp(state.PodEntries{
			req.PodUid: state.ContainerEntries{req.ContainerName: allocationInfo},
		}); err != nil {
			return nil, fmt.Errorf("allocate rejected illegal reclaimed ramp-up for %s/%s: %w",
				req.PodUid, req.ContainerName, err)
		}
	}
	if existingAllocationSatisfiesRequest(allocationInfo, reqInt, p.isRampUpReclaimHardPartitionEnabled()) &&
		!util.PodInplaceUpdateResizing(req) {
		general.InfoS("already allocated and meet requirement",
			"podNamespace", req.PodNamespace,
			"podName", req.PodName,
			"containerName", req.ContainerName,
			"numCPUs", reqInt,
			"originalAllocationResult", allocationInfo.OriginalAllocationResult.String(),
			"currentResult", allocationInfo.AllocationResult.String())

		resp, err = cpuutil.PackAllocationResponse(allocationInfo, string(v1.ResourceCPU), util.OCIPropertyNameCPUSetCPUs,
			false, true, req, allocationInfo.Annotations)
		if err != nil {
			general.Errorf("pod: %s/%s, container: %s PackResourceAllocationResponseByAllocationInfo failed with error: %v",
				req.PodNamespace, req.PodName, req.ContainerName, err)
			return nil, fmt.Errorf("PackResourceAllocationResponseByAllocationInfo failed with error: %v", err)
		}
		p.clearCPUSetInAllocationResponseIfNeeded(resp, allocationInfo)

		return resp, nil
	}

	if p.allocation.allocationHandlers[qosLevel] == nil {
		return nil, fmt.Errorf("katalyst QoS level: %s is not supported yet", qosLevel)
	}
	return p.allocation.allocationHandlers[qosLevel](ctx, req, false)
}

// AllocateForPod is called during pod admit so that the resource
// plugin can allocate corresponding resource for the pod
// according to resource request
func (p *DynamicPolicy) AllocateForPod(ctx context.Context,
	req *pluginapi.PodResourceRequest,
) (resp *pluginapi.PodResourceAllocationResponse, respErr error) {
	return nil, util.ErrNotImplemented
}

// PreStartContainer is called, if indicated by resource plugin during registration phase,
// before each container start. Resource plugin can run resource specific operations
// such as resetting the resource before making resources available to the container
func (p *DynamicPolicy) PreStartContainer(context.Context,
	*pluginapi.PreStartContainerRequest,
) (*pluginapi.PreStartContainerResponse, error) {
	return nil, nil
}

func (p *DynamicPolicy) RemovePod(ctx context.Context,
	req *pluginapi.RemovePodRequest,
) (resp *pluginapi.RemovePodResponse, err error) {
	if req == nil {
		return nil, fmt.Errorf("RemovePod got nil req")
	}
	general.InfoS("called", "podUID", req.PodUid)

	startTime := time.Now()
	p.Lock()
	defer func() {
		p.Unlock()
		if err != nil {
			_ = p.emitter.emitter.StoreInt64(util.MetricNameRemovePodFailed, 1, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "error_message", Val: metric.MetricTagValueFormat(err)})
			general.ErrorS(err, "RemovePod failed", "podUID", req.PodUid)
		}
		general.InfoS("finished", "duration", time.Since(startTime).String(), "podUID", req.PodUid)
	}()

	releaseExecution, err := p.acquireCPUSetAdjustmentExecutionLeaseLocked(ctx, "RemovePod")
	if err != nil {
		return nil, err
	}
	defer releaseExecution.release()
	ctx = context.WithValue(ctx, cpuSetAdjustmentExecutionLeaseContextKey{}, releaseExecution)

	currentPodEntries := p.state.GetPodEntries()
	if len(currentPodEntries[req.PodUid]) == 0 {
		if err := p.accompanyReg().ReleaseAccompanyResource(req); err != nil {
			general.ErrorS(err, "failed to release accompany resource", "podUID", req.PodUid)
			return nil, fmt.Errorf("failed to release accompany resource: %w", err)
		}
		return &pluginapi.RemovePodResponse{}, nil
	}

	if p.config.enableCPUAdvisor {
		if p.advisor.advisorClient == nil {
			return nil, fmt.Errorf("cpu advisor client is nil")
		}
		_, err = p.advisor.advisorClient.RemovePod(ctx, &advisorsvc.RemovePodRequest{PodUid: req.PodUid})
		if err != nil {
			return nil, fmt.Errorf("remove pod in QoS aware server failed with error: %v", err)
		}
	}

	expectedRevision := p.state.GetRevision()
	podEntries := currentPodEntries.Clone()
	delete(podEntries, req.PodUid)
	machineState, err := generateMachineStateFromPodEntries(
		p.machine.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
	if err != nil {
		return nil, fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	err = p.adjustAllocationEntriesAfterDeletionAtRevisionWithContext(
		ctx, podEntries, machineState, true, expectedRevision)
	if err != nil {
		general.ErrorS(err, "adjustAllocationEntries failed", "podUID", req.PodUid)
		return nil, fmt.Errorf("commit pod removal failed: %w", err)
	}

	if err := p.accompanyReg().ReleaseAccompanyResource(req); err != nil {
		general.ErrorS(err, "failed to release accompany resource", "podUID", req.PodUid)
		return nil, fmt.Errorf("failed to release accompany resource: %w", err)
	}

	return &pluginapi.RemovePodResponse{}, nil
}

func (p *DynamicPolicy) removeContainer(podUID, containerName string, persistCheckpoint bool) error {
	podEntries := p.state.GetPodEntries()

	found := false
	if podEntries[podUID][containerName] != nil {
		found = true
	}

	delete(podEntries[podUID], containerName)

	if !found {
		return nil
	}

	updatedMachineState, err := generateMachineStateFromPodEntries(p.machine.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
	if err != nil {
		return fmt.Errorf("GenerateMachineStateFromPodEntries failed with error: %v", err)
	}

	if err := p.state.SetPodEntries(podEntries, false); err != nil {
		return fmt.Errorf("set pod entries after removing container: %w", err)
	}
	if err := p.state.SetMachineState(updatedMachineState, false); err != nil {
		return fmt.Errorf("set machine state after removing container: %w", err)
	}
	if persistCheckpoint {
		return p.state.StoreState()
	}
	return nil
}

// initAdvisorClientConn initializes cpu-advisor related connections
func (p *DynamicPolicy) initAdvisorClientConn() (err error) {
	cpuAdvisorConn, err := process.Dial(
		p.config.cpuAdvisorSocketAbsPath,
		5*time.Second,
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
			// add metadata to outgoing context to indicate that qrm supports GetAdvice.
			// advisor that also supports GetAdvice will ignore such AddContainer/RemovePod requests.
			ctx = metadata.AppendToOutgoingContext(ctx, util.AdvisorRPCMetadataKeySupportsGetAdvice, util.AdvisorRPCMetadataValueSupportsGetAdvice)
			return invoker(ctx, method, req, reply, cc, opts...)
		}),
	)
	if err != nil {
		err = fmt.Errorf("get cpu advisor connection with socket: %s failed with error: %v", p.config.cpuAdvisorSocketAbsPath, err)
		return
	}

	p.advisor.advisorClient = advisorapi.NewCPUAdvisorClient(cpuAdvisorConn)
	p.advisor.advisorConn = cpuAdvisorConn
	return nil
}

func (p *DynamicPolicy) initHintOptimizers() error {
	var err error
	hintOptimizerFactoryOptions := p.generateHintOptimizerFactoryOptions()

	p.hintOpt.sharedCoresNUMABindingHintOptimizer, err = registry.SharedCoresHintOptimizerRegistry.HintOptimizerWithFilters(
		p.config.conf.SharedCoresHintOptimizerPolicies,
		p.config.conf.SharedCoresHintFilterPolicies,
		hintOptimizerFactoryOptions,
	)
	if err != nil {
		return fmt.Errorf("SharedCoresHintOptimizerRegistry.HintOptimizerWithFilters failed with error: %v", err)
	}

	p.hintOpt.dedicatedCoresNUMABindingHintOptimizer, err = registry.DedicatedCoresHintOptimizerRegistry.HintOptimizer(p.config.conf.DedicatedCoresHintOptimizerPolicies,
		p.generateHintOptimizerFactoryOptions())
	if err != nil {
		return fmt.Errorf("DedicatedCoresHintOptimizerRegistry.HintOptimizer failed with error: %v", err)
	}

	return nil
}

func (p *DynamicPolicy) generateHintOptimizerFactoryOptions() policy.HintOptimizerFactoryOptions {
	return policy.HintOptimizerFactoryOptions{
		Conf:                   p.config.conf,
		Emitter:                p.emitter.emitter,
		MetaServer:             p.meta.metaServer,
		ResourcePackageManager: p.resourcePackageManager,
		State:                  p.state,
		ReservedCPUs:           p.config.reservedCPUs,
	}
}

// cleanPools is used to clean pools-related data in local state
func (p *DynamicPolicy) cleanPools() error {
	podEntries := p.state.GetPodEntries()
	poolsToDelete := p.cleanPoolsFromPodEntries(podEntries)
	if poolsToDelete.Len() == 0 {
		general.Infof("there is no pool to delete")
		return nil
	}

	general.Infof("pools to delete: %v", poolsToDelete.UnsortedList())
	machineState, err := generateMachineStateFromPodEntries(p.machine.machineInfo.CPUTopology, podEntries, p.state.GetMachineState())
	if err != nil {
		return fmt.Errorf("calculate machineState by podEntries failed with error: %v", err)
	}

	if err := p.state.SetPodEntries(podEntries, false); err != nil {
		return fmt.Errorf("set cleaned pool entries: %w", err)
	}
	if err := p.state.SetMachineState(machineState, false); err != nil {
		return fmt.Errorf("set cleaned pool machine state: %w", err)
	}
	if err := p.state.StoreState(); err != nil {
		return fmt.Errorf("store cleaned pool state: %w", err)
	}
	return nil
}

func (p *DynamicPolicy) cleanPoolsFromPodEntries(podEntries state.PodEntries) sets.String {
	remainPools := make(map[string]bool)
	numaResourcePackagePinnedCPUSet := p.state.GetMachineState().GetNUMAResourcePackagePinnedCPUSet()

	// walk through pod entries to put them into specified pool maps
	for podUID, entries := range podEntries {
		if entries.IsPoolEntry() {
			continue
		}

		for containerName, allocationInfo := range entries {
			if allocationInfo == nil {
				general.Warningf("pod %s container %s allocation info is nil during cleanPools, skip it", podUID, containerName)
				continue
			}
			ownerPool := allocationInfo.GetOwnerPoolName()
			if ownerPool == commonstate.EmptyOwnerPoolName && !allocationInfo.RampUp {
				if allocationInfo.CheckSharedNUMABinding() {
					var err error
					ownerPool, _, err = state.GetCanonicalSharedNUMABindingPoolKey(
						numaResourcePackagePinnedCPUSet, allocationInfo)
					if err != nil {
						general.Warningf("pod %s container %s has invalid legacy shared NUMA-binding ownership during cleanPools: %v; skip cleanup",
							podUID, containerName, err)
						return sets.NewString()
					}
				} else {
					ownerPool = allocationInfo.GetPoolName()
				}
			}
			if ownerPool != commonstate.EmptyOwnerPoolName {
				remainPools[ownerPool] = true
			}
		}
	}

	// when default share residual backfill is enabled, the share pool is
	// synthesized without any owning container, so it must be retained here.
	keepSyntheticDefaultShare := false
	if p != nil && p.config.dynamicConfig != nil {
		dynamicConfig := p.config.dynamicConfig.GetDynamicConfiguration()
		keepSyntheticDefaultShare = dynamicConfig != nil &&
			dynamicConfig.FillDefaultSharePoolWithNonReclaimCPUs
	}

	// if pool exists in entries, but has no corresponding container, we need to delete it
	poolsToDelete := sets.NewString()
	for poolName, entries := range podEntries {
		if entries.IsPoolEntry() {
			// system pool is managed separately, should skip it
			if commonstate.IsSystemPool(poolName) {
				continue
			}
			// when default share residual backfill is enabled, the share pool is
			// synthesized without any owning container, so retain it here instead of
			// unconditionally adding share to state.ResidentPools (which would change
			// the legacy behavior when the gate is disabled).
			if keepSyntheticDefaultShare && poolName == commonstate.PoolNameShare {
				continue
			}
			if !remainPools[poolName] && !state.ResidentPools.Has(poolName) {
				poolsToDelete.Insert(poolName)
			}
		}
	}

	for _, poolName := range poolsToDelete.UnsortedList() {
		delete(podEntries, poolName)
	}
	return poolsToDelete
}

// initReservePool initializes reserve pool for system cores workload
func (p *DynamicPolicy) initReservePool() error {
	reserveAllocationInfo := p.state.GetAllocationInfo(commonstate.PoolNameReserve, commonstate.FakedContainerName)
	if reserveAllocationInfo != nil && !reserveAllocationInfo.AllocationResult.IsEmpty() {
		general.Infof("pool: %s allocation result transform from %s to %s",
			commonstate.PoolNameReserve, reserveAllocationInfo.AllocationResult.String(), p.config.reservedCPUs)
	}

	general.Infof("initReservePool %s: %s", commonstate.PoolNameReserve, p.config.reservedCPUs)
	topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, p.config.reservedCPUs)
	if err != nil {
		return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, result cpuset: %s, error: %v",
			commonstate.PoolNameReserve, p.config.reservedCPUs.String(), err)
	}

	curReserveAllocationInfo := &state.AllocationInfo{
		AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReserve),
		AllocationResult:                 p.config.reservedCPUs.Clone(),
		OriginalAllocationResult:         p.config.reservedCPUs.Clone(),
		TopologyAwareAssignments:         topologyAwareAssignments,
		OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
	}
	return p.state.SetAllocationInfo(
		commonstate.PoolNameReserve, commonstate.FakedContainerName, curReserveAllocationInfo, true)
}

// initReclaimPool initializes pools for reclaimed-cores.
// if this info already exists in state-file, just use it, otherwise calculate right away
func (p *DynamicPolicy) initReclaimPool() error {
	// for reclaimed pool, we must make them exist when the node isn't in hybrid mode even if cause overlap
	allAvailableCPUs := p.machine.machineInfo.CPUDetails.CPUs().Difference(p.config.reservedCPUs)
	numaIDs := p.machine.machineInfo.CPUDetails.NUMANodes().ToSliceInt()
	cpusPerCore := p.machine.machineInfo.CPUTopology.CPUsPerCore()
	capacityByNUMA := make(map[int]int, len(numaIDs))
	for _, numaID := range numaIDs {
		candidates := allAvailableCPUs.Intersection(
			p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID))
		capacityByNUMA[numaID] = takeCoreAlignedCPUSet(
			p.machine.machineInfo.CPUTopology, candidates, machine.NewCPUSet(), candidates.Size(),
		).Size()
	}
	reclaimTargets, err := machine.DistributeConfiguredHardReclaimFloor(
		capacityByNUMA, nil, p.reclaim.reservedReclaimedCPUsSize, cpusPerCore)
	if err != nil {
		return fmt.Errorf("distribute core-aligned reserved reclaim cpus: %w", err)
	}
	availableNUMAIDs := make([]int, 0, len(numaIDs))
	for _, numaID := range numaIDs {
		if reclaimTargets[numaID] > 0 {
			availableNUMAIDs = append(availableNUMAIDs, numaID)
		}
	}
	defaultReservedReclaimedCPUSet := machine.NewCPUSet()
	for _, numaID := range availableNUMAIDs {
		target := reclaimTargets[numaID]
		numaCPUs := p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID)
		selected := takeCoreAlignedCPUSet(
			p.machine.machineInfo.CPUTopology,
			allAvailableCPUs.Intersection(numaCPUs),
			numaCPUs,
			target,
		)
		if selected.Size() != target {
			return fmt.Errorf("select core-aligned reserved reclaim cpus in NUMA %d: got %d, want %d",
				numaID, selected.Size(), target)
		}
		defaultReservedReclaimedCPUSet = defaultReservedReclaimedCPUSet.Union(selected)
	}
	p.reclaim.reservedReclaimedCPUSet = defaultReservedReclaimedCPUSet.Clone()
	p.reclaim.reservedReclaimedCPUsSize = defaultReservedReclaimedCPUSet.Size()

	defaultReservedTopologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, defaultReservedReclaimedCPUSet)
	if err != nil {
		return fmt.Errorf("unable to calculate defaultReservedTopologyAwareAssignments for pool: %s, "+
			"result cpuset: %s, error: %v", commonstate.PoolNameReclaim, defaultReservedReclaimedCPUSet.String(), err)
	}
	p.reclaim.reservedReclaimedTopologyAwareAssignments = machine.DeepcopyCPUAssignment(defaultReservedTopologyAwareAssignments)

	reclaimedAllocationInfo := p.state.GetAllocationInfo(commonstate.PoolNameReclaim, commonstate.FakedContainerName)
	if reclaimedAllocationInfo == nil {
		podEntries := p.state.GetPodEntries()
		noneResidentCPUs := podEntries.GetFilteredPoolsCPUSet(state.ResidentPools)

		machineState := p.state.GetMachineState()
		availableCPUs := machineState.GetFilteredAvailableCPUSet(p.config.reservedCPUs,
			func(ai *state.AllocationInfo) bool {
				return ai.CheckDedicated() || ai.CheckSharedNUMABinding()
			},
			state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicatedNUMABinding)).Difference(noneResidentCPUs)

		reclaimedCPUSet := machine.NewCPUSet()
		for _, numaID := range availableNUMAIDs {
			target := p.reclaim.reservedReclaimedTopologyAwareAssignments[numaID].Size()
			candidates := availableCPUs.Intersection(p.machine.machineInfo.CPUDetails.CPUsInNUMANodes(numaID))
			selected := takeCoreAlignedCPUSet(
				p.machine.machineInfo.CPUTopology,
				candidates,
				p.reclaim.reservedReclaimedTopologyAwareAssignments[numaID],
				target,
			)
			reclaimedCPUSet = reclaimedCPUSet.Union(selected)
		}

		if shortfall := p.reclaim.reservedReclaimedCPUsSize - reclaimedCPUSet.Size(); shortfall > 0 {
			selected := takeCoreAlignedCPUSet(
				p.machine.machineInfo.CPUTopology,
				availableCPUs.Difference(reclaimedCPUSet),
				p.reclaim.reservedReclaimedCPUSet.Difference(reclaimedCPUSet),
				shortfall,
			)
			reclaimedCPUSet = reclaimedCPUSet.Union(selected)
		}

		if reclaimedCPUSet.Size() != p.reclaim.reservedReclaimedCPUsSize {
			return fmt.Errorf("select core-aligned reclaim cpus: got %d, want %d",
				reclaimedCPUSet.Size(), p.reclaim.reservedReclaimedCPUsSize)
		}

		general.Infof("initReclaimPool %s: %s", commonstate.PoolNameReclaim, reclaimedCPUSet.String())
		topologyAwareAssignments, err := machine.GetNumaAwareAssignments(p.machine.machineInfo.CPUTopology, reclaimedCPUSet)
		if err != nil {
			return fmt.Errorf("unable to calculate topologyAwareAssignments for pool: %s, "+
				"result cpuset: %s, error: %v", commonstate.PoolNameReclaim, reclaimedCPUSet.String(), err)
		}

		curPoolAllocationInfo := &state.AllocationInfo{
			AllocationMeta:                   commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameReclaim),
			AllocationResult:                 reclaimedCPUSet.Clone(),
			OriginalAllocationResult:         reclaimedCPUSet.Clone(),
			TopologyAwareAssignments:         topologyAwareAssignments,
			OriginalTopologyAwareAssignments: machine.DeepcopyCPUAssignment(topologyAwareAssignments),
		}
		if err := p.state.SetAllocationInfo(
			commonstate.PoolNameReclaim, commonstate.FakedContainerName, curPoolAllocationInfo, true); err != nil {
			return fmt.Errorf("persist initial reclaim pool: %w", err)
		}
	} else {
		general.Infof("exist initial %s: %s", commonstate.PoolNameReclaim, reclaimedAllocationInfo.AllocationResult.String())
	}

	return nil
}

func (p *DynamicPolicy) initInterruptPool() error {
	interruptAllocationInfo := p.state.GetAllocationInfo(commonstate.PoolNameInterrupt, commonstate.FakedContainerName)
	if interruptAllocationInfo == nil {
		allocationInfo := &state.AllocationInfo{
			AllocationMeta: commonstate.GenerateGenericPoolAllocationMeta(commonstate.PoolNameInterrupt),
		}
		if err := p.state.SetAllocationInfo(
			commonstate.PoolNameInterrupt, commonstate.FakedContainerName, allocationInfo, true); err != nil {
			return fmt.Errorf("persist initial interrupt pool: %w", err)
		}
	} else {
		general.Infof("exist initial %s: %s", commonstate.PoolNameInterrupt, interruptAllocationInfo.AllocationResult.String())
	}

	return nil
}

// getContainerRequestedCores parses and returns request cores for the given container
func (p *DynamicPolicy) getContainerRequestedCores(allocationInfo *state.AllocationInfo) float64 {
	return cpuutil.GetContainerRequestedCores(p.meta.metaServer, allocationInfo)
}

func (p *DynamicPolicy) checkNonBindingShareCoresCpuResource(req *pluginapi.ResourceRequest) (bool, error) {
	_, reqFloat64, err := util.GetPodAggregatedRequestResource(req)
	if err != nil {
		return false, fmt.Errorf("GetQuantityFromResourceReq failed with error: %v", err)
	}

	shareCoresAllocatedInt := state.GetNonBindingSharedRequestedQuantityFromPodEntries(p.state.GetPodEntries(), map[string]float64{req.PodUid: reqFloat64}, p.getContainerRequestedCores)

	machineState := p.state.GetMachineState()
	pooledCPUs := machineState.GetFilteredAvailableCPUSet(p.config.reservedCPUs,
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckDedicated),
		state.WrapAllocationMetaFilter((*commonstate.AllocationMeta).CheckSharedOrDedicatedNUMABinding))

	general.Infof("[checkNonBindingShareCoresCpuResource] node cpu allocated: %d, allocatable: %d", shareCoresAllocatedInt, pooledCPUs.Size())
	if shareCoresAllocatedInt > pooledCPUs.Size() {
		general.Warningf("[checkNonBindingShareCoresCpuResource] no enough cpu resource for non-binding share cores pod: %s/%s, container: %s (request: %.02f, node allocated: %d, node allocatable: %d)",
			req.PodNamespace, req.PodName, req.ContainerName, reqFloat64, shareCoresAllocatedInt, pooledCPUs.Size())
		return false, nil
	}

	general.InfoS("checkNonBindingShareCoresCpuResource cpu successfully",
		"podNamespace", req.PodNamespace,
		"podName", req.PodName,
		"containerName", req.ContainerName,
		"request", reqFloat64)

	return true, nil
}

func (p *DynamicPolicy) applySidecarAllocationInfoFromMainContainer(sidecarAllocationInfo, mainAllocationInfo *state.AllocationInfo) bool {
	changed := false
	if sidecarAllocationInfo.RampUp != mainAllocationInfo.RampUp ||
		sidecarAllocationInfo.OwnerPoolName != mainAllocationInfo.OwnerPoolName ||
		!sidecarAllocationInfo.AllocationResult.Equals(mainAllocationInfo.AllocationResult) ||
		!sidecarAllocationInfo.OriginalAllocationResult.Equals(mainAllocationInfo.OriginalAllocationResult) ||
		!state.CheckAllocationInfoTopologyAwareAssignments(sidecarAllocationInfo, mainAllocationInfo) ||
		!state.CheckAllocationInfoOriginTopologyAwareAssignments(sidecarAllocationInfo, mainAllocationInfo) {

		sidecarAllocationInfo.RampUp = mainAllocationInfo.RampUp
		sidecarAllocationInfo.OwnerPoolName = mainAllocationInfo.OwnerPoolName
		sidecarAllocationInfo.AllocationResult = mainAllocationInfo.AllocationResult.Clone()
		sidecarAllocationInfo.OriginalAllocationResult = mainAllocationInfo.OriginalAllocationResult.Clone()
		sidecarAllocationInfo.TopologyAwareAssignments = machine.DeepcopyCPUAssignment(mainAllocationInfo.TopologyAwareAssignments)
		sidecarAllocationInfo.OriginalTopologyAwareAssignments = machine.DeepcopyCPUAssignment(mainAllocationInfo.OriginalTopologyAwareAssignments)

		changed = true
	}

	// Copy annotations from main container
	if sidecarAllocationInfo.Annotations == nil && len(mainAllocationInfo.Annotations) > 0 {
		sidecarAllocationInfo.Annotations = make(map[string]string, len(mainAllocationInfo.Annotations))
	}
	for key, value := range mainAllocationInfo.Annotations {
		if sidecarAllocationInfo.Annotations[key] != value {
			sidecarAllocationInfo.Annotations[key] = value
			changed = true
		}
	}

	request := p.getContainerRequestedCores(sidecarAllocationInfo)
	if sidecarAllocationInfo.RequestQuantity != request {
		sidecarAllocationInfo.RequestQuantity = request
		changed = true
	}

	return changed
}

// RegisterAllocationHook registers a hook that is called before allocation info is updated.
// It is concurrency-safe.
func (p *DynamicPolicy) RegisterAllocationHook(hook AllocationHook) {
	p.Lock()
	defer p.Unlock()
	p.allocation.allocationHooks = append(p.allocation.allocationHooks, hook)
}

// invokeAllocationHooks triggers all registered allocation hooks.
// Note: This method must be called with the lock held by the caller if concurrency protection is needed.
// We avoid internal locking here to prevent potential deadlocks when called from methods that already hold the lock.
func (p *DynamicPolicy) invokeAllocationHooks(oldAllocationInfo, newAllocationInfo *state.AllocationInfo) error {
	for _, hook := range p.allocation.allocationHooks {
		if err := hook(oldAllocationInfo, newAllocationInfo); err != nil {
			return err
		}
	}
	return nil
}

// invokeAllocationHooksForPodEntries triggers allocation hooks for non-pool containers before committing to state.
// Note: This method must be called with the lock held by the caller to ensure state consistency
// and avoid deadlocks due to nested locking.
func (p *DynamicPolicy) invokeAllocationHooksForPodEntries(curEntries, newEntries state.PodEntries) error {
	if len(p.allocation.allocationHooks) == 0 {
		return nil
	}

	for podUID, containerEntries := range newEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for containerName, newAllocationInfo := range containerEntries {
			var oldAllocationInfo *state.AllocationInfo
			// retrieve old allocation info from curEntries directly to avoid the overhead
			// of GetAllocationInfo which involves Clone() operations.
			if curContainerEntries, ok := curEntries[podUID]; ok {
				oldAllocationInfo = curContainerEntries[containerName]
			}
			if err := p.invokeAllocationHooks(oldAllocationInfo, newAllocationInfo); err != nil {
				return fmt.Errorf("invokeAllocationHooks failed for pod: %s, container: %s: %v", podUID, containerName, err)
			}
		}
	}
	return nil
}

// updateAllocationInfo wraps state.SetAllocationInfo with hook execution.
// If no hooks are registered, it avoids the overhead of retrieving the old allocation info.
func (p *DynamicPolicy) updateAllocationInfo(podUID, containerName string, oldAllocationInfo, allocationInfo *state.AllocationInfo, persist bool) error {
	if len(p.allocation.allocationHooks) > 0 {
		if oldAllocationInfo == nil {
			oldAllocationInfo = p.state.GetAllocationInfo(podUID, containerName)
		}
		if err := p.invokeAllocationHooks(oldAllocationInfo, allocationInfo); err != nil {
			return err
		}
	}

	// Defensive: a synchronous legacy allocation hook may mutate the resulting
	// allocationInfo (e.g. set RampUp on a reclaimed container). Reuse the single
	// authoritative predicate so an illegal reclaimed RampUp cannot be persisted
	// via this legacy direct-write path.
	if allocationInfo != nil {
		if err := validateNoReclaimedRampUp(state.PodEntries{
			podUID: state.ContainerEntries{containerName: allocationInfo},
		}); err != nil {
			return fmt.Errorf("updateAllocationInfo rejected illegal reclaimed ramp-up for %s/%s: %w",
				podUID, containerName, err)
		}
	}

	return p.state.SetAllocationInfo(podUID, containerName, allocationInfo, persist)
}

// shouldBypassCPUSetAdjustment reports whether response cpuset backfill should
// be skipped for shared_cores, reclaimed_cores and system_cores pods.
func (p *DynamicPolicy) shouldBypassCPUSetAdjustment() bool {
	if p.config.dynamicConfig == nil {
		return false
	}
	dyn := p.config.dynamicConfig.GetDynamicConfiguration()
	return dyn != nil && dyn.EnableBypassCPUSetAdjustment
}

func (p *DynamicPolicy) shouldBypassCPUSetAdjustmentForAllocation(allocationInfo *state.AllocationInfo) bool {
	return p.shouldBypassCPUSetAdjustment() &&
		allocationInfo != nil &&
		(allocationInfo.CheckShared() || allocationInfo.CheckReclaimed() || allocationInfo.CheckSystem())
}

// clearCPUSetInAllocation clears the cpuset string on every entry of a
// *pluginapi.ResourceAllocation in place, leaving TopologyAssignments,
// AllocatedQuantity, ResourceHints and Annotations untouched. It is a no-op
// when the input is nil.
func clearCPUSetInAllocation(alloc *pluginapi.ResourceAllocation) {
	if alloc == nil {
		return
	}
	info := alloc.ResourceAllocation[string(v1.ResourceCPU)]
	if info != nil {
		info.AllocationResult = ""
	}
}

func (p *DynamicPolicy) clearCPUSetInAllocationResponseIfNeeded(resp *pluginapi.ResourceAllocationResponse, allocationInfo *state.AllocationInfo) {
	if resp == nil || !p.shouldBypassCPUSetAdjustmentForAllocation(allocationInfo) {
		return
	}
	clearCPUSetInAllocation(resp.AllocationResult)
}
