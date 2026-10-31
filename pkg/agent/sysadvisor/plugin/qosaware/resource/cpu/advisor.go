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

package cpu

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/klog/v2"

	configapi "github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	"github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/headroomassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/assembler/provisionassembler"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/cputypes"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/isolation"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/headroompolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region/provisionpolicy"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// todo:
// 1. Support dedicated without and with numa binding but non numa exclusive containers

// metric names for cpu advisor
const (
	metricCPUAdvisorPoolSize           = "cpu_advisor_pool_size"
	metricCPUAdvisorPoolQuota          = "cpu_advisor_pool_quota"
	metricCPUAdvisorUpdateDuration     = "cpu_advisor_update_duration"
	metricRegionStatus                 = "region_status"
	metricRegionIndicatorTargetPrefix  = "region_indicator_target_"
	metricRegionIndicatorCurrentPrefix = "region_indicator_current_"
	metricRegionIndicatorErrorPrefix   = "region_indicator_error_"

	metricCPUAdvisorMachineAllocatable       = "cpu_advisor_machine_allocatable"
	metricCPUAdvisorReserveSize              = "cpu_advisor_reserve_size"
	metricCPUAdvisorFixedPoolSize            = "cpu_advisor_fixed_pool_size"
	metricCPUAdvisorRawReclaimSize           = "cpu_advisor_raw_reclaim_size"
	metricCPUAdvisorFinalReclaimSize         = "cpu_advisor_final_reclaim_size"
	metricCPUAdvisorReclaimClampedSize       = "cpu_advisor_reclaim_clamped_size"
	metricCPUAdvisorDedicatedSize            = "cpu_advisor_dedicated_size"
	metricCPUAdvisorIsolationSize            = "cpu_advisor_isolation_size"
	metricCPUAdvisorCustomSharedSize         = "cpu_advisor_custom_shared_size"
	metricCPUAdvisorSNBSize                  = "cpu_advisor_snb_size"
	metricCPUAdvisorExclusiveNUMASize        = "cpu_advisor_exclusive_numa_size"
	metricCPUAdvisorPinnedCPUSize            = "cpu_advisor_pinned_cpu_size"
	metricCPUAdvisorDefaultShareBefore       = "cpu_advisor_default_share_before_backfill"
	metricCPUAdvisorDefaultShareBackfilled   = "cpu_advisor_default_share_backfilled"
	metricCPUAdvisorDefaultShareFinal        = "cpu_advisor_default_share_final"
	metricCPUAdvisorUnassignedNonReclaimSize = "cpu_advisor_unassigned_non_reclaim_size"

	// Per-scope reclaim constraint diagnostics. These report the internal
	// rate-limited ceiling / target bookkeeping; they are NOT the published pool
	// size (see cpu_advisor_pool_size for that).
	metricReclaimScopeCeiling       = "cpu_advisor_reclaim_scope_ceiling"
	metricReclaimScopeDesired       = "cpu_advisor_reclaim_scope_desired"
	metricReclaimScopeFloor         = "cpu_advisor_reclaim_scope_floor"
	metricReclaimScopeSteadyCap     = "cpu_advisor_reclaim_scope_steady_cap"
	metricReclaimScopePhase         = "cpu_advisor_reclaim_scope_phase"
	metricReclaimScopeLastPublished = "cpu_advisor_reclaim_scope_last_published"
	metricReclaimScopeTargetError   = "cpu_advisor_reclaim_scope_target_error_total"
	metricReclaimScopeAckStalled    = "cpu_advisor_reclaim_scope_ack_stalled_total"
	metricReclaimScopeUnackedCycles = "cpu_advisor_reclaim_scope_unacked_cycles"
	metricReclaimScopeAcked         = "cpu_advisor_reclaim_scope_acked"

	cpuAdvisorHealthCheckName     = "cpu_advisor_update"
	healthCheckTolerationDuration = 30 * time.Second
)

var errIsolationSafetyCheckFailed = fmt.Errorf("isolation safety check failed")

func init() {
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyNone, provisionpolicy.NewPolicyNone)
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyCanonical, provisionpolicy.NewPolicyCanonical)
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyRama, provisionpolicy.NewPolicyRama)
	provisionpolicy.RegisterInitializer(types.CPUProvisionPolicyDynamicQuota, provisionpolicy.NewPolicyDynamicQuota)

	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyNone, headroompolicy.NewPolicyNone)
	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyCanonical, headroompolicy.NewPolicyCanonical)
	headroompolicy.RegisterInitializer(types.CPUHeadroomPolicyNUMADedicated, headroompolicy.NewPolicyNUMADedicated)

	provisionassembler.RegisterInitializer(types.CPUProvisionAssemblerCommon, provisionassembler.NewProvisionAssemblerCommon)

	headroomassembler.RegisterInitializer(types.CPUHeadroomAssemblerCommon, headroomassembler.NewHeadroomAssemblerCommon)
	// TODO: CPUHeadroomAssemblerDedicated policy has removed, its name is retained for compatibility.
	headroomassembler.RegisterInitializer(types.CPUHeadroomAssemblerDedicated, headroomassembler.NewHeadroomAssemblerCommon)
}

// cpuResourceAdvisor is the entrance of updating cpu resource provision advice for
// all qos regions, and merging them into cpu provision result to notify cpu server.
// Smart algorithms and calculators could be adopted to give accurate realtime resource
// provision hint for each region.
type cpuResourceAdvisor struct {
	conf      *config.Configuration
	extraConf interface{}
	period    time.Duration

	advisorUpdated    bool
	lastCommittedConf *dynamic.Configuration

	regionMap              map[string]region.QoSRegion // map[regionName]region
	reservedForReclaim     map[int]int                 // map[numaID]reservedForReclaim
	rampUpReclaimCPUSetCap map[int]int                 // map[numaID]rampUpReclaimCPUSetCap
	rampUpDomainTargets    map[cputypes.ReclaimConstraintScope]types.ReclaimConstraintTarget
	numaAvailable          map[int]int    // map[numaID]availableResource
	numRegionsPerNuma      map[int]int    // map[numaID]regionQuantity
	nonBindingNumas        machine.CPUSet // numas without numa binding pods

	// The overlap flags AllowSharedCoresOverlapReclaimedCores /
	// DisableDedicatedCoresOverlapReclaimedCores are intentionally NOT persisted
	// here. They are resolved per-cycle from dynamicConf and injected once into the
	// ProvisionContext (the single authoritative source); caching them on the advisor
	// would create a stale copy a later cycle could read.
	reclaimConstraintGuard reclaimConstraintGuard

	provisionAssembler provisionassembler.ProvisionAssembler
	headroomAssembler  headroomassembler.HeadroomAssembler

	isolator        isolation.Isolator
	isolationSafety bool

	mutex      sync.RWMutex
	metaCache  metacache.MetaCache
	metaServer *metaserver.MetaServer
	emitter    metrics.MetricEmitter
}

// NewCPUResourceAdvisor returns a cpuResourceAdvisor instance
func NewCPUResourceAdvisor(conf *config.Configuration, extraConf interface{}, metaCache metacache.MetaCache,
	metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter,
) *cpuResourceAdvisor {
	cra := &cpuResourceAdvisor{
		conf:      conf,
		extraConf: extraConf,
		period:    conf.QoSAwarePluginConfiguration.SyncPeriod,

		advisorUpdated: false,

		regionMap:              make(map[string]region.QoSRegion),
		reservedForReclaim:     make(map[int]int),
		rampUpReclaimCPUSetCap: make(map[int]int),
		rampUpDomainTargets:    make(map[cputypes.ReclaimConstraintScope]types.ReclaimConstraintTarget),
		numaAvailable:          make(map[int]int),
		numRegionsPerNuma:      make(map[int]int),
		nonBindingNumas:        machine.NewCPUSet(),

		isolator: isolation.NewLoadIsolator(conf, extraConf, emitter, metaCache, metaServer),

		metaCache:  metaCache,
		metaServer: metaServer,
		emitter:    emitter,
	}

	dynamicConf := conf.GetDynamicConfiguration()
	if err := cra.updateReservedForReclaim(dynamicConf); err != nil {
		klog.Errorf("[qosaware-cpu] initialize reserved resource for reclaim failed: %v", err)
	}

	if err := cra.initializeProvisionAssembler(); err != nil {
		klog.Errorf("[qosaware-cpu] initialize provision assembler failed: %v", err)
	}
	if err := cra.initializeHeadroomAssembler(); err != nil {
		klog.Errorf("[qosaware-cpu] initialize headroom assembler failed: %v", err)
	}

	return cra
}

func (cra *cpuResourceAdvisor) Run(ctx context.Context) {
	<-ctx.Done()
}

func (cra *cpuResourceAdvisor) GetHeadroom() (resource.Quantity, map[int]resource.Quantity, error) {
	startTime := time.Now()
	klog.Infof("[qosaware-cpu] receive get headroom request")

	cra.mutex.RLock()
	general.InfoS("acquired lock", "duration", time.Since(startTime))
	defer cra.mutex.RUnlock()
	defer func() {
		general.InfoS("finished", "duration", time.Since(startTime))
	}()

	if !cra.advisorUpdated {
		klog.Infof("[qosaware-cpu] skip getting headroom: advisor not updated")
		return resource.Quantity{}, nil, fmt.Errorf("advisor not updated")
	}

	if cra.headroomAssembler == nil {
		klog.Errorf("[qosaware-cpu] get headroom failed: no legal assembler")
		return resource.Quantity{}, nil, fmt.Errorf("no legal assembler")
	}

	headroom, numaHeadroom, err := cra.headroomAssembler.GetHeadroom(cra.lastCommittedConf)
	if err != nil {
		klog.Errorf("[qosaware-cpu] get headroom failed: %v", err)
	} else {
		klog.InfoS("get headroom", "headroom", headroom, "numaHeadroom", numaHeadroom)
	}

	return headroom, numaHeadroom, err
}

func (cra *cpuResourceAdvisor) UpdateAndGetAdvice(_ context.Context) (interface{}, error) {
	startTime := time.Now()
	result, err := cra.update()
	_ = general.UpdateHealthzStateByError(cpuAdvisorHealthCheckName, err)
	general.InfoS("finished", "duration", time.Since(startTime))
	return result, err
}

// update works in a monolithic way to maintain lifecycle and triggers update actions for all regions;
// todo: re-consider whether it's efficient or we should make start individual goroutine for each region
func (cra *cpuResourceAdvisor) update() (*types.CPUAdvice, error) {
	startTime := time.Now()
	cra.mutex.Lock()
	general.InfoS("acquired lock", "duration", time.Since(startTime))
	defer cra.mutex.Unlock()

	metadataSnapshot := cra.snapshotRegionAssignments()
	updateSucceeded := false
	defer func() {
		if updateSucceeded {
			return
		}
		cra.advisorUpdated = false
		cra.regionMap = make(map[string]region.QoSRegion)
		cra.restoreRegionAssignments(metadataSnapshot)
	}()

	dynamicConf := cra.conf.GetDynamicConfiguration()
	if dynamicConf == nil {
		return nil, fmt.Errorf("dynamic configuration is nil")
	}
	hardEnabled := dynamicConf.EnableReclaim &&
		dynamicConf.EnableRampUpReclaimHardPartition &&
		metadataSnapshot.rampUpDomains.Len() > 0
	maxRampUpStep := cra.conf.CPUAdvisorConfiguration.MaxRampUpStep

	// Per-scope reclaim constraint: derive the scope->NUMA mapping from the
	// current region map, then activate only the scopes whose NUMAs host an
	// active ramp-up domain. Observed reclaim is broken down by NUMA and
	// aggregated per scope so each scope carries an independent ACK.
	scopeNumas := cra.scopeNUMAMapping(dynamicConf.DisableDedicatedCoresOverlapReclaimedCores)
	var (
		reclaimConstraint   cputypes.ReclaimConstraint
		reclaimCeilings     map[cputypes.ReclaimConstraintScope]*int
		reclaimActiveScopes map[cputypes.ReclaimConstraintScope]bool
		reclaimAccounting   map[cputypes.ReclaimConstraintScope]decidedScope
		// liveDomainScopes carries the scopes that own a live ramp-up domain this
		// cycle. reclaimActiveScopes (from constraint()) additionally folds in the
		// draining scopes the guard still clamps; commit() must see the live active
		// set so a draining scope is tagged phase=draining (not acked) and its
		// desired is reset to steadyCap.
		liveDomainScopes = map[cputypes.ReclaimConstraintScope]bool{}
	)
	// The guard runs whenever ramp-up hard partition is active OR whenever it still
	// holds draining scope state (a scope that must step its ceiling back up to
	// steadyCap after ramp-up exited). A scope with no active domain and no state
	// collapses to ReclaimConstraintNone inside constraint().
	if hardEnabled || cra.reclaimConstraintGuard.hasRetainedState() {
		liveDomainScopes = cra.activeReclaimScopes(scopeNumas, metadataSnapshot.rampUpDomains)
		observedByScope, observedOKByScope := cra.observedReclaimByScope(scopeNumas)
		reclaimConstraint, reclaimCeilings, reclaimActiveScopes, reclaimAccounting = cra.reclaimConstraintGuard.constraint(
			liveDomainScopes, observedByScope, observedOKByScope, maxRampUpStep, cra.cpusPerCore())
	} else {
		reclaimConstraint = cputypes.ReclaimConstraintNone
	}

	result, err := cra.updateWithIsolationGuardian(
		dynamicConf, metadataSnapshot.rampUpDomains.Len() > 0, hardEnabled, metadataSnapshot.steadyExclusiveNUMAs,
		metadataSnapshot.rampUpDomains, reclaimConstraint, reclaimCeilings, reclaimActiveScopes, true)
	if err != nil {
		if err == errIsolationSafetyCheckFailed {
			klog.Warningf("[qosaware-cpu] failed to updateWithIsolationGuardian(true): %q", err)
			result, err = cra.updateWithIsolationGuardian(
				dynamicConf, metadataSnapshot.rampUpDomains.Len() > 0, hardEnabled, metadataSnapshot.steadyExclusiveNUMAs,
				metadataSnapshot.rampUpDomains, reclaimConstraint, reclaimCeilings, reclaimActiveScopes, false)
		}
		if err != nil {
			return nil, err
		}
	}
	cra.updateRegionEntries()
	cra.lastCommittedConf = dynamicConf
	cra.advisorUpdated = true
	cra.updateRegionStatus()
	cra.emitMetrics(*result)
	cra.reclaimConstraintGuard.commit(
		liveDomainScopes,
		reclaimCeilings,
		reclaimAccounting,
		result.ReclaimConstraintTargets,
		cra.publishedReclaimByScope(result, scopeNumas),
		scopeNumas,
	)
	updateSucceeded = true
	general.InfoS("committed cpu reclaim constraint",
		"constraint", reclaimConstraint,
		"ceilings", reclaimCeilings,
		"activeScopes", reclaimActiveScopes,
		"excess", result.ReclaimConstraintExcess,
		"hardEnabled", hardEnabled)
	general.InfoS("finished", "duration", time.Since(startTime))
	// Carry the overlap flags sampled from this cycle's single dynamicConf out
	// with the result. The server publishes them to the proto from this same
	// sampling instead of re-reading config, so computation and publish can never
	// drift apart across a config swap.
	return &types.CPUAdvice{
		CalculationResult:                          result,
		AllowSharedCoresOverlapReclaimedCores:      dynamicConf.AllowSharedCoresOverlapReclaimedCores,
		DisableDedicatedCoresOverlapReclaimedCores: dynamicConf.DisableDedicatedCoresOverlapReclaimedCores,
	}, nil
}

// cpusPerCore returns the node SMT width used to keep every per-cycle ceiling
// movement and domain target whole-core, defaulting to 1 when the topology is
// unavailable.
func (cra *cpuResourceAdvisor) cpusPerCore() int {
	if cra.metaServer != nil && cra.metaServer.CPUTopology != nil {
		if c := cra.metaServer.CPUTopology.CPUsPerCore(); c > 0 {
			return c
		}
	}
	return 1
}

// scopeNUMAMapping derives the stable mapping from each ReclaimConstraintScope
// to the set of NUMA ids it covers. Every real NUMA belongs to exactly one
// scope:
//   - a NUMA with an exclusive dedicated region      -> exclusive/{regionName}
//   - a NUMA with a legacy-exclusive region          -> legacy-exclusive/{regionName}
//   - a NUMA with a non-exclusive dedicated binding  -> non-exclusive/{numaID}
//   - every remaining (non-binding) NUMA             -> non-exclusive/-1
//
// disableDedicatedCoresOverlap is the per-cycle resolved flag injected by the
// caller (it comes straight from dynamicConf): it decides whether a NUMA-
// exclusive dedicated region is the disjoint "exclusive" scope or the legacy
// overlapping "legacy-exclusive" scope. It is passed in rather than read from a
// persistent advisor field so the mapping cannot observe a stale value.
//
// The mapping is read from the current regionMap; on the first cycle (empty
// region map) only the global non-exclusive scope exists, which is a safe
// passthrough baseline. Region names are sorted for deterministic iteration.
func (cra *cpuResourceAdvisor) scopeNUMAMapping(disableDedicatedCoresOverlap bool) map[cputypes.ReclaimConstraintScope][]int {
	mapping := make(map[cputypes.ReclaimConstraintScope][]int)
	claimed := sets.NewInt()

	regionNames := make([]string, 0, len(cra.regionMap))
	for name := range cra.regionMap {
		regionNames = append(regionNames, name)
	}
	sort.Strings(regionNames)

	for _, name := range regionNames {
		r := cra.regionMap[name]
		if r.Type() != configapi.QoSRegionTypeDedicated || !r.IsNumaBinding() {
			continue
		}
		var scope cputypes.ReclaimConstraintScope
		if r.IsNumaExclusive() {
			if disableDedicatedCoresOverlap {
				scope = cputypes.NewExclusiveReclaimConstraintScope(name)
			} else {
				scope = cputypes.NewLegacyExclusiveReclaimConstraintScope(name)
			}
		} else {
			// Non-exclusive NUMA-binding dedicated (SNB): each binding NUMA is
			// its own scope because the reclaim pool on that NUMA is pinned to
			// the dedicated workload.
			for _, numaID := range r.GetBindingNumas().ToSliceInt() {
				if claimed.Has(numaID) {
					continue
				}
				claimed.Insert(numaID)
				s := cputypes.NewNonExclusiveReclaimConstraintScope(numaID)
				mapping[s] = append(mapping[s], numaID)
			}
			continue
		}
		for _, numaID := range r.GetBindingNumas().ToSliceInt() {
			if claimed.Has(numaID) {
				continue
			}
			claimed.Insert(numaID)
			mapping[scope] = append(mapping[scope], numaID)
		}
	}

	// Every NUMA not claimed by a dedicated binding scope belongs to the
	// global non-binding (faked) scope.
	globalScope := cputypes.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)
	for _, numaID := range cra.metaServer.CPUDetails.NUMANodes().ToSliceInt() {
		if claimed.Has(numaID) {
			continue
		}
		mapping[globalScope] = append(mapping[globalScope], numaID)
	}
	return mapping
}

// activeReclaimScopes returns the subset of scopes whose NUMA set intersects
// the active ramp-up domains. The global (faked) scope is activated only by
// the FakedNUMAID domain; a real-NUMA ramp-up never activates the global
// scope, and vice versa.
func (cra *cpuResourceAdvisor) activeReclaimScopes(
	scopeNumas map[cputypes.ReclaimConstraintScope][]int,
	rampUpDomains sets.Int,
) map[cputypes.ReclaimConstraintScope]bool {
	active := make(map[cputypes.ReclaimConstraintScope]bool)
	globalScope := cputypes.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)
	for scope, numas := range scopeNumas {
		if scope == globalScope {
			if rampUpDomains.Has(commonstate.FakedNUMAID) {
				active[scope] = true
			}
			continue
		}
		for _, numaID := range numas {
			if rampUpDomains.Has(numaID) {
				active[scope] = true
				break
			}
		}
	}
	return active
}

// observedReclaimByScope reads the current reclaim pool assignment from
// metaCache and aggregates the per-NUMA cpuset size into each scope. A
// missing or nil pool yields empty maps (no scope is ACK-eligible).
func (cra *cpuResourceAdvisor) observedReclaimByScope(
	scopeNumas map[cputypes.ReclaimConstraintScope][]int,
) (map[cputypes.ReclaimConstraintScope]int, map[cputypes.ReclaimConstraintScope]bool) {
	observed := make(map[cputypes.ReclaimConstraintScope]int)
	observedOK := make(map[cputypes.ReclaimConstraintScope]bool)
	if cra == nil || cra.metaCache == nil {
		return observed, observedOK
	}
	poolInfo, ok := cra.metaCache.GetPoolInfo(commonstate.PoolNameReclaim)
	if !ok || poolInfo == nil {
		return observed, observedOK
	}
	for scope, numas := range scopeNumas {
		total := 0
		for _, numaID := range numas {
			if cpuset, ok := poolInfo.TopologyAwareAssignments[numaID]; ok {
				total += cpuset.Size()
			}
		}
		observed[scope] = total
		observedOK[scope] = true
	}
	return observed, observedOK
}

// publishedReclaimByScope aggregates the published reclaim PoolEntries (keyed
// by NUMA id, including FakedNUMAID for the global pool) into each scope. A
// NUMA not covered by the mapping is skipped with a warning.
func (cra *cpuResourceAdvisor) publishedReclaimByScope(
	result *types.InternalCPUCalculationResult,
	scopeNumas map[cputypes.ReclaimConstraintScope][]int,
) map[cputypes.ReclaimConstraintScope]int {
	published := make(map[cputypes.ReclaimConstraintScope]int)
	if result == nil {
		return published
	}
	numaToScope := make(map[int]cputypes.ReclaimConstraintScope)
	for scope, numas := range scopeNumas {
		for _, numaID := range numas {
			numaToScope[numaID] = scope
		}
	}
	// The global non-binding pool is published as a single aggregate entry keyed by
	// FakedNUMAID, while the scope's member list holds its backing real NUMAs. Map
	// the faked id onto the global scope explicitly so the aggregate publication is
	// recorded as that scope's ACK baseline; otherwise the global scope never
	// acknowledges and its ceiling cannot advance.
	globalScope := cputypes.NewNonExclusiveReclaimConstraintScope(commonstate.FakedNUMAID)
	if _, hasGlobal := scopeNumas[globalScope]; hasGlobal {
		numaToScope[commonstate.FakedNUMAID] = globalScope
	}
	for numaID, entry := range result.PoolEntries[commonstate.PoolNameReclaim] {
		scope, ok := numaToScope[numaID]
		if !ok {
			klog.Warningf("[qosaware-cpu] published reclaim on NUMA %d not covered by scope mapping, skipping", numaID)
			continue
		}
		published[scope] += entry.Size
	}
	return published
}

type containerRegionAssignment struct {
	regionNames sets.String
	isolated    bool
}

type regionAssignmentSnapshot struct {
	containers map[string]map[string]containerRegionAssignment
	pools      map[string]sets.String
	// rampUpDomains is the set of reclaim domains that host an active ramp-up
	// container at cycle start. A domain is either FakedNUMAID (-1) for the
	// global domain (a non-NUMA-binding shared ramp-up), or a real NUMA id for a
	// NUMA-binding ramp-up pinned to that NUMA. This replaces the former
	// node-global activeRampUp bool so a ramp-up on one NUMA cannot raise the
	// reclaim reservation on another NUMA's dedicated pool.
	rampUpDomains        sets.Int
	steadyExclusiveNUMAs sets.Int
}

func (cra *cpuResourceAdvisor) snapshotRegionAssignments() regionAssignmentSnapshot {
	snapshot := regionAssignmentSnapshot{
		containers:           make(map[string]map[string]containerRegionAssignment),
		pools:                make(map[string]sets.String),
		rampUpDomains:        sets.NewInt(),
		steadyExclusiveNUMAs: sets.NewInt(),
	}
	cra.metaCache.RangeContainer(func(podUID, containerName string, ci *types.ContainerInfo) bool {
		if snapshot.containers[podUID] == nil {
			snapshot.containers[podUID] = make(map[string]containerRegionAssignment)
		}
		if ci.RampUp {
			// Derive the ramp-up reclaim domain per container. A container whose
			// domain cannot be resolved unambiguously is dropped (fail closed)
			// rather than widening ramp-up to the whole node.
			domains, err := cra.rampUpReclaimDomainsForContainer(ci)
			if err != nil {
				klog.Warningf("[qosaware-cpu] dropping ramp-up reclaim domain for %s/%s: %v",
					ci.PodUID, ci.ContainerName, err)
			} else {
				snapshot.rampUpDomains = snapshot.rampUpDomains.Union(domains)
			}
		} else if ci.IsDedicatedNumaExclusive() {
			// Reclaimability is intentionally irrelevant here. Once an
			// exclusive DNB is steady, its NUMA keeps the finalized reserve and
			// must not inherit another NUMA's active ramp-up target.
			for numaID := range ci.TopologyAwareAssignments {
				snapshot.steadyExclusiveNUMAs.Insert(numaID)
			}
		}
		snapshot.containers[podUID][containerName] = containerRegionAssignment{
			regionNames: sets.NewString(ci.RegionNames.List()...),
			isolated:    ci.Isolated,
		}
		return true
	})
	cra.metaCache.RangePool(func(poolName string, poolInfo *types.PoolInfo) bool {
		snapshot.pools[poolName] = sets.NewString(poolInfo.RegionNames.List()...)
		return true
	})
	return snapshot
}

// rampUpReclaimDomainsForContainer derives the reclaim domains a single ramp-up
// container may influence. It mirrors QRM's AllocationInfo.RampUpReclaimDomains
// but operates on the SysAdvisor ContainerInfo view.
//
// Ramp-up must be scoped to the domain that actually hosts the ramp-up workload,
// so a ramp-up on one NUMA cannot raise the reclaim reservation on another NUMA's
// dedicated pool:
//   - non-NUMA-binding ramp-up  -> the global domain (FakedNUMAID, -1), which only
//     backs the global shared/reclaim pool and must not write a per-NUMA floor;
//   - NUMA-binding ramp-up with materialized placement -> every placement NUMA
//     (dedicated may legitimately span NUMAs; a shared ramp-up spanning NUMAs is
//     ambiguous and fails closed);
//   - NUMA-binding ramp-up without placement -> resolved from the explicit NUMA hint;
//   - anything missing or out of topology range -> an error (fail closed).
func (cra *cpuResourceAdvisor) rampUpReclaimDomainsForContainer(ci *types.ContainerInfo) (sets.Int, error) {
	if ci == nil {
		return nil, fmt.Errorf("rampUpReclaimDomains got nil containerInfo")
	}

	// non-binding shared ramp-up always belongs to the global domain.
	if !ci.IsNumaBinding() {
		return sets.NewInt(commonstate.FakedNUMAID), nil
	}

	// topologyNUMAs validates placements/hints against the real NUMA set. It is
	// left empty when topology is unavailable (e.g. lightweight unit tests), in
	// which case the structural domain derivation below is still honored.
	topologyNUMAs := sets.NewInt()
	if cra.metaServer != nil && cra.metaServer.CPUDetails != nil {
		topologyNUMAs.Insert(cra.metaServer.CPUDetails.NUMANodes().ToSliceInt()...)
	}

	placement := make([]int, 0, len(ci.TopologyAwareAssignments))
	for numaID := range ci.TopologyAwareAssignments {
		placement = append(placement, numaID)
	}

	if len(placement) == 0 {
		// The ramp-up allocation has not been materialized yet; fall back to the
		// explicit NUMA hint. A committed ramp-up container should already carry
		// TopologyAwareAssignments, so falling back here is itself a warning-level
		// inconsistency rather than a silent whole-node propagation.
		hintNUMA, err := ci.GetActualNUMABindingResult()
		if err != nil {
			return nil, fmt.Errorf("missing ramp-up domain: %s/%s has no placement and unreadable NUMA hint: %v",
				ci.PodUID, ci.ContainerName, err)
		}
		if hintNUMA == commonstate.FakedNUMAID || (topologyNUMAs.Len() > 0 && !topologyNUMAs.Has(hintNUMA)) {
			return nil, fmt.Errorf("out-of-range ramp-up domain: %s/%s NUMA hint %d is not a real NUMA",
				ci.PodUID, ci.ContainerName, hintNUMA)
		}
		return sets.NewInt(hintNUMA), nil
	}

	if len(placement) > 1 {
		// A dedicated NUMA-binding allocation may legitimately span NUMAs; every
		// placement NUMA is its own reclaim domain. A shared NUMA-binding ramp-up
		// spanning NUMAs is ambiguous (shared cores are pinned to a single NUMA).
		if !ci.IsDedicatedNumaBinding() {
			return nil, fmt.Errorf("ambiguous ramp-up domain: shared numa-binding ramp-up %s/%s spans NUMAs %v",
				ci.PodUID, ci.ContainerName, placement)
		}
		domains := sets.NewInt()
		for _, numaID := range placement {
			if topologyNUMAs.Len() > 0 && !topologyNUMAs.Has(numaID) {
				return nil, fmt.Errorf("out-of-range ramp-up domain: %s/%s placed on NUMA %d",
					ci.PodUID, ci.ContainerName, numaID)
			}
			domains.Insert(numaID)
		}
		return domains, nil
	}

	numaID := placement[0]
	if topologyNUMAs.Len() > 0 && !topologyNUMAs.Has(numaID) {
		return nil, fmt.Errorf("out-of-range ramp-up domain: %s/%s placed on NUMA %d",
			ci.PodUID, ci.ContainerName, numaID)
	}
	return sets.NewInt(numaID), nil
}

// restoreRegionAssignments rolls the metadata cache back to the snapshot taken at the start of the cycle.
//
// It deliberately restores only the fields that update() mutates while wiring regions to containers and
// pools: ContainerInfo.RegionNames / ContainerInfo.Isolated and PoolInfo.RegionNames. These are the sole
// pieces of shared metadata a failed cycle can leave half-written, so reverting them (and rebuilding
// regionMap from scratch) is enough to guarantee the next cycle starts from a clean, consistent state.
// Other ContainerInfo / PoolInfo fields (requests, topology-aware assignments, etc.) are owned by other
// producers and are not touched by region assignment, so restoring them here would risk clobbering
// legitimately newer values.
func (cra *cpuResourceAdvisor) restoreRegionAssignments(snapshot regionAssignmentSnapshot) {
	_ = cra.metaCache.RangeAndUpdateContainer(func(podUID, containerName string, ci *types.ContainerInfo) bool {
		assignment, ok := snapshot.containers[podUID][containerName]
		if !ok {
			ci.RegionNames = sets.NewString()
			ci.Isolated = false
			return true
		}
		ci.RegionNames = sets.NewString(assignment.regionNames.List()...)
		ci.Isolated = assignment.isolated
		return true
	})

	_ = cra.metaCache.RangeAndUpdatePool(func(poolName string, poolInfo *types.PoolInfo) bool {
		regionNames, ok := snapshot.pools[poolName]
		if !ok {
			regionNames = sets.NewString()
		}
		poolInfo.RegionNames = sets.NewString(regionNames.List()...)
		return true
	})
}

// If updateWithIsolationGuardian fails with isolation enabled, we should try again with isolation disabled.
// todo: we should re-design the mechanism of isolation instead of disabling this functionality
func (cra *cpuResourceAdvisor) updateWithIsolationGuardian(dynamicConf *dynamic.Configuration,
	rampUpActive bool,
	hardActive bool,
	steadyExclusiveNUMAs sets.Int,
	rampUpDomains sets.Int,
	reclaimConstraint cputypes.ReclaimConstraint,
	reclaimCeilings map[cputypes.ReclaimConstraintScope]*int,
	reclaimActiveScopes map[cputypes.ReclaimConstraintScope]bool,
	tryIsolation bool,
) (
	*types.InternalCPUCalculationResult,
	error,
) {
	startTime := time.Now()
	defer func(t time.Time) {
		elapsed := time.Since(t)
		_ = cra.emitter.StoreFloat64(metricCPUAdvisorUpdateDuration, float64(elapsed/time.Millisecond), metrics.MetricTypeNameRaw)
		klog.Infof("[qosaware-cpu] update duration %v", elapsed)
	}(startTime)

	// sanity check: if reserve pool exists
	reservePoolInfo, ok := cra.metaCache.GetPoolInfo(commonstate.PoolNameReserve)
	if !ok || reservePoolInfo == nil {
		klog.Errorf("[qosaware-cpu] skip update: reserve pool does not exist")
		return nil, fmt.Errorf("reserve pool does not exist")
	}

	if err := cra.updateNumasAvailableResource(dynamicConf, hardActive, steadyExclusiveNUMAs, rampUpDomains); err != nil {
		klog.Errorf("[qosaware-cpu] update NUMA available resource failed: %v", err)
		return nil, fmt.Errorf("failed to update NUMA available resource: %w", err)
	}
	isolationExists := cra.setIsolatedContainers(tryIsolation)

	// assign containers to regions
	if err := cra.assignContainersToRegions(); err != nil {
		klog.Errorf("[qosaware-cpu] assign containers to regions failed: %q", err)
		return nil, fmt.Errorf("failed to assign containers to regions: %q", err)
	}

	cra.gcRegionMap()
	cra.updateAdvisorEssentials(dynamicConf)
	if tryIsolation && isolationExists && !cra.checkIsolationSafety() {
		klog.Errorf("[qosaware-cpu] failed to check isolation")
		return nil, errIsolationSafetyCheckFailed
	}

	pinnedCPUSizeByNuma, pinnedCPUSizeByPackageByNuma, err := cra.getPinnedCPUSizes()
	if err != nil {
		klog.Errorf("[qosaware-cpu] failed to get pinned cpu sizes: %v", err)
		return nil, err
	}

	// run an episode of provision and headroom policy update for each region
	for _, r := range cra.regionMap {
		r.SetEssentials(types.ResourceEssentials{
			DynamicConfiguration: dynamicConf,
			EnableReclaim:        dynamicConf.EnableReclaim,
			ResourceUpperBound:   cra.getRegionMaxRequirement(r, pinnedCPUSizeByNuma, pinnedCPUSizeByPackageByNuma),
			ResourceLowerBound:   cra.getRegionMinRequirement(r),
			ReservedForReclaim:   cra.getRegionReservedForReclaim(r),
			ReservedForAllocate:  cra.getRegionReservedForAllocate(dynamicConf, r),

			AllowSharedCoresOverlapReclaimedCores:      dynamicConf.AllowSharedCoresOverlapReclaimedCores,
			DisableDedicatedCoresOverlapReclaimedCores: dynamicConf.DisableDedicatedCoresOverlapReclaimedCores,
		})

		r.TryUpdateProvision()
		r.TryUpdateHeadroom()
	}
	if klog.V(6).Enabled() {
		klog.Infof("[qosaware-cpu] region map: %v", general.ToString(cra.regionMap))
	}

	// assemble provision result from each region
	calculationResult, err := cra.assembleProvision(
		dynamicConf, rampUpActive, rampUpDomains, reclaimConstraint, reclaimCeilings, reclaimActiveScopes)
	if err != nil {
		klog.Errorf("[qosaware-cpu] assemble provision failed: %q", err)
		return nil, fmt.Errorf("failed to assemble provisioner: %q", err)
	}

	return &calculationResult, nil
}

func (cra *cpuResourceAdvisor) getPinnedCPUSizes() (map[int]int, map[string]map[int]int, error) {
	cfg := cra.metaCache.GetResourcePackageConfig()
	pinnedCPUSizeByNuma := make(map[int]int)
	pinnedCPUSizeByPackageByNuma := make(map[string]map[int]int)
	for numaID, pkgMap := range cfg {
		for pkgName, state := range pkgMap {
			if state == nil {
				continue
			}
			size := state.PinnedCPUSet.Size()
			if size <= 0 {
				continue
			}
			pinnedCPUSizeByNuma[numaID] += size
			if _, ok := pinnedCPUSizeByPackageByNuma[pkgName]; !ok {
				pinnedCPUSizeByPackageByNuma[pkgName] = make(map[int]int)
			}
			pinnedCPUSizeByPackageByNuma[pkgName][numaID] = size
		}
	}
	return pinnedCPUSizeByNuma, pinnedCPUSizeByPackageByNuma, nil
}

// setIsolatedContainers get isolation status from isolator and update into containers
func (cra *cpuResourceAdvisor) setIsolatedContainers(enableIsolated bool) bool {
	isolatedPods := sets.NewString()
	if enableIsolated {
		isolatedPods = sets.NewString(cra.isolator.GetIsolatedPods()...)
	}
	if len(isolatedPods) > 0 {
		klog.Infof("[qosaware-cpu] current isolated pod: %v", isolatedPods.List())
	}

	_ = cra.metaCache.RangeAndUpdateContainer(func(podUID string, _ string, ci *types.ContainerInfo) bool {
		ci.Isolated = false
		if isolatedPods.Has(podUID) {
			ci.Isolated = true
		}
		return true
	})
	return len(isolatedPods) > 0
}

// checkIsolationSafety returns true iff the isolated-limit-sum and share-pool-size exceed total capacity
// todo: this logic contains a lot of assumptions and should be refined in the future
func (cra *cpuResourceAdvisor) checkIsolationSafety() bool {
	shareAndIsolationPoolSize := 0
	dedicatedNonExclusivePoolSize := 0
	nonBindingNumas := cra.metaServer.CPUDetails.NUMANodes()
	for _, r := range cra.regionMap {
		if r.Type() == configapi.QoSRegionTypeShare {
			controlKnob, err := r.GetProvision()
			if err != nil {
				klog.Errorf("[qosaware-cpu] get controlKnob for %v err: %v", r.Name(), err)
				return false
			}
			shareAndIsolationPoolSize += int(controlKnob[configapi.ControlKnobNonReclaimedCPURequirement].Value)
		} else if r.Type() == configapi.QoSRegionTypeIsolation {
			pods := r.GetPods()
			cra.metaCache.RangeContainer(func(podUID string, _ string, containerInfo *types.ContainerInfo) bool {
				if _, ok := pods[podUID]; ok {
					shareAndIsolationPoolSize += int(containerInfo.CPULimit)
				}
				return true
			})
		} else if r.Type() == configapi.QoSRegionTypeDedicated {
			if r.IsNumaExclusive() {
				nonBindingNumas = nonBindingNumas.Difference(r.GetBindingNumas())
			} else if r.IsNumaBinding() {
				// dedicated numa-binding non-exclusive region, calculate the pool size based on binding numas
				dedicatedNonExclusivePoolSize += int(math.Ceil(r.GetPodsRequest() / float64(r.GetBindingNumas().Size())))
			} else {
				// dedicated non-numa-binding non-exclusive region, calculate the pool size based on pods request
				dedicatedNonExclusivePoolSize += int(math.Ceil(r.GetPodsRequest()))
			}
		}
	}

	nonExclusiveSize := cra.metaServer.NUMAToCPUs.CPUSizeInNUMAs(cra.nonBindingNumas.ToSliceNoSortInt()...)
	klog.Infof("[qosaware-cpu] shareAndIsolationPoolSize %v, nonExclusiveSize %v，dedicatedNonExclusivePoolSize %v",
		shareAndIsolationPoolSize, nonExclusiveSize, dedicatedNonExclusivePoolSize)
	if shareAndIsolationPoolSize+dedicatedNonExclusivePoolSize > nonExclusiveSize {
		return false
	}
	return true
}

// assignContainersToRegions re-construct regions every time (instead of an incremental way),
// and this requires metaCache to ensure data integrity
func (cra *cpuResourceAdvisor) assignContainersToRegions() error {
	var errList []error

	// clear containers for all regions
	for _, r := range cra.regionMap {
		r.Clear()
	}

	// sync containers
	f := func(podUID string, containerName string, ci *types.ContainerInfo) bool {
		regions, err := cra.assignToRegions(ci)
		if err != nil {
			errList = append(errList, err)
		}
		if regions == nil {
			return true
		}

		// update region pod set and region map
		for _, r := range regions {
			if err := r.AddContainer(ci); err != nil {
				errList = append(errList, err)
				return true
			}
			// region may be set in regionMap for multiple times, and it is reentrant
			cra.regionMap[r.Name()] = r
		}

		// update container info
		cra.setContainerRegions(ci, regions)

		// update pool info
		if ci.OwnerPoolName == commonstate.PoolNameDedicated {
			// dedicated pool should not exist in metaCache.poolEntries
			return true
		} else if ci.Isolated || cra.conf.IsolationForceEnablePools.Has(ci.OriginOwnerPoolName) {
			// isolated pool should not exist in metaCache.poolEntries
			return true
		} else {
			// todo currently, we may call setPoolRegions multiple time, and we
			//  depend on the reentrant of it, need to refine
			if err := cra.setPoolRegions(ci.OriginOwnerPoolName, regions); err != nil {
				errList = append(errList, err)
				return true
			}
		}

		return true
	}
	_ = cra.metaCache.RangeAndUpdateContainer(f)

	return errors.NewAggregate(errList)
}

// assignToRegions returns the region list for the given container;
// may need to construct region structures if they don't exist.
func (cra *cpuResourceAdvisor) assignToRegions(ci *types.ContainerInfo) ([]region.QoSRegion, error) {
	if ci == nil {
		return nil, fmt.Errorf("container info is nil")
	}

	switch ci.QoSLevel {
	case consts.PodAnnotationQoSLevelSharedCores:
		return cra.assignShareContainerToRegions(ci)
	case consts.PodAnnotationQoSLevelDedicatedCores:
		return cra.assignDedicatedContainerToRegions(ci)
	default:
		return nil, nil
	}
}

func (cra *cpuResourceAdvisor) assignShareContainerToRegions(ci *types.ContainerInfo) ([]region.QoSRegion, error) {
	// Ramp-up containers have no stable owner pool yet. This applies equally
	// to NUMA-binding and non-NUMA-binding shared containers.
	if ci.RampUp {
		return nil, nil
	}

	numaID := commonstate.FakedNUMAID
	if cra.conf.GenericSysAdvisorConfiguration.EnableShareCoresNumaBinding && ci.IsNumaBinding() {
		if ci.OwnerPoolName == "" {
			return nil, fmt.Errorf("empty owner pool name, %v/%v", ci.PodUID, ci.ContainerName)
		}

		if len(ci.TopologyAwareAssignments) != 1 {
			return nil, fmt.Errorf("invalid topology aware assignments of container: %s/%s", ci.PodUID, ci.ContainerName)
		}

		for key := range ci.TopologyAwareAssignments {
			numaID = key
		}
	} else {
		// ignore the share pods without requests info
		if ci.OwnerPoolName == "" && math.Abs(ci.CPURequest) < 1e9 {
			return nil, nil
		}

		// return error if container owner pool name is empty
		if !ci.RampUp && ci.OwnerPoolName == "" {
			return nil, fmt.Errorf("empty owner pool name, %v/%v", ci.PodUID, ci.ContainerName)
		}
	}

	// assign isolated container
	if ci.Isolated || cra.conf.IsolationForceEnablePools.Has(ci.OriginOwnerPoolName) {
		regionName := ""
		if cra.conf.IsolationNonExclusivePools.Has(ci.OriginOwnerPoolName) {
			// use origin owner pool name as region name, because all the container in this pool
			// share only one region which is non-exclusive
			regionName = ci.OriginOwnerPoolName

			// if there already exists a non-exclusive isolation region for this pod, just reuse it
			regions := cra.getPoolRegions(regionName)
			if len(regions) > 0 {
				return regions, nil
			}

			// if there already exists a region with same name as this region, just reuse it
			regions = cra.getRegionsByRegionNames(sets.NewString(regionName))
			if len(regions) > 0 {
				return regions, nil
			}
		} else {
			// if there already exists an isolation region for this pod, just reuse it
			regions, err := cra.getContainerRegions(ci, configapi.QoSRegionTypeIsolation)
			if err != nil {
				return nil, err
			} else if len(regions) > 0 {
				return regions, nil
			}
		}

		r := region.NewQoSRegionIsolation(ci, regionName, cra.conf, cra.extraConf, numaID, cra.metaCache, cra.metaServer, cra.emitter)
		klog.Infof("create a new isolation region (%s/%s) for container %s/%s", r.OwnerPoolName(), r.Name(), ci.PodUID, ci.ContainerName)
		return []region.QoSRegion{r}, nil
	}

	// assign shared cores container. focus on pool.
	// Why OriginOwnerPoolName ?
	// Case 1: a new container
	//	OriginOwnerPoolName == OwnerPoolName
	// Case 2: put the isolation container back to share pool
	// 	OriginOwnerPoolName != OwnerPoolName:
	// Case others:
	//	OriginOwnerPoolName == OwnerPoolName
	regions := cra.getPoolRegions(ci.OriginOwnerPoolName)
	if len(regions) > 0 {
		return regions, nil
	}

	// create one region by owner pool name
	r := region.NewQoSRegionShare(ci, cra.conf, cra.extraConf, numaID, cra.metaCache, cra.metaServer, cra.emitter)
	klog.Infof("create a new share region (%s/%s) for container %s/%s", r.OwnerPoolName(), r.Name(), ci.PodUID, ci.ContainerName)
	return []region.QoSRegion{r}, nil
}

func (cra *cpuResourceAdvisor) assignDedicatedContainerToRegions(ci *types.ContainerInfo) ([]region.QoSRegion, error) {
	// assign dedicated cores numa exclusive containers. focus on container.
	regions, err := cra.getContainerRegions(ci, configapi.QoSRegionTypeDedicated)
	if err != nil {
		return nil, err
	} else if len(regions) > 0 {
		return regions, nil
	}
	if ci.IsNumaBinding() {
		// create regions by numa node
		for numaID := range ci.TopologyAwareAssignments {
			r := region.NewQoSRegionDedicated(ci, cra.conf, numaID, cra.extraConf, cra.metaCache, cra.metaServer, cra.emitter)
			regions = append(regions, r)
		}
	} else {
		r := region.NewQoSRegionDedicated(ci, cra.conf, commonstate.FakedNUMAID, cra.extraConf, cra.metaCache, cra.metaServer, cra.emitter)
		regions = append(regions, r)
	}
	return regions, nil
}

// gcRegionMap deletes empty regions in region map
func (cra *cpuResourceAdvisor) gcRegionMap() {
	for regionName, r := range cra.regionMap {
		if r.IsEmpty() {
			delete(cra.regionMap, regionName)
			klog.Infof("[qosaware-cpu] delete region %v", regionName)
		}
	}
}

// updateAdvisorEssentials updates following essentials after assigning containers to regions:
// 1. non-binding numas, i.e. numas without numa binding containers
// 2. binding numas of non numa binding regions
// 3. region quantity of each numa
func (cra *cpuResourceAdvisor) updateAdvisorEssentials(dynamicConf *dynamic.Configuration) {
	cra.nonBindingNumas = cra.metaServer.CPUDetails.NUMANodes()
	// The overlap flags are deliberately not cached here; they are read straight
	// from dynamicConf at each per-cycle injection point (SetEssentials,
	// scopeNUMAMapping, ProvisionContext) so no stale advisor copy exists.

	// update non-binding numas
	for _, r := range cra.regionMap {
		if !r.IsNumaBinding() {
			continue
		}
		// ignore isolation region
		if r.Type() == configapi.QoSRegionTypeDedicated || r.Type() == configapi.QoSRegionTypeShare {
			cra.nonBindingNumas = cra.nonBindingNumas.Difference(r.GetBindingNumas())
		}
	}

	// reset region quantity
	for _, numaID := range cra.metaServer.CPUDetails.NUMANodes().ToSliceInt() {
		cra.numRegionsPerNuma[numaID] = 0
	}

	for _, r := range cra.regionMap {
		// set binding numas for non numa binding regions
		if !r.IsNumaBinding() && r.Type() == configapi.QoSRegionTypeShare {
			r.SetBindingNumas(cra.nonBindingNumas)
		}

		// accumulate region quantity for each numa
		for _, numaID := range r.GetBindingNumas().ToSliceInt() {
			cra.numRegionsPerNuma[numaID] += 1
		}
	}
}

// assembleProvision generates internal calculation result.
// must make sure pool names from cpu provision following qrm definition;
// numa ID set as -1 means no numa-preference is needed.
//
// rampUpDomains is the cycle-start set of active ramp-up reclaim domains
// (FakedNUMAID -1 = global/non-binding, otherwise a real NUMA id). It is
// converted to a sorted []int and forwarded into InternalCPUCalculationResult
// so the cpu server can gate the live-reclaim floor per domain instead of
// trusting the node-global RampUpActive flag.
func (cra *cpuResourceAdvisor) assembleProvision(dynamicConf *dynamic.Configuration,
	rampUpActive bool,
	rampUpDomains sets.Int,
	reclaimConstraint cputypes.ReclaimConstraint,
	reclaimCeilings map[cputypes.ReclaimConstraintScope]*int,
	reclaimActiveScopes map[cputypes.ReclaimConstraintScope]bool,
) (types.InternalCPUCalculationResult, error) {
	if cra.provisionAssembler == nil {
		return types.InternalCPUCalculationResult{}, fmt.Errorf("no legal provision assembler")
	}

	// Snapshot the QRM-committed reclaim pool sizes per NUMA at cycle start.
	// The assembler uses this as a best-effort lower bound under domain-scoped
	// ramp-up protection, but never at the expense of dedicated guarantees.
	liveReclaimByNUMA := make(map[int]int)
	if cra.metaCache != nil {
		if poolInfo, ok := cra.metaCache.GetPoolInfo(commonstate.PoolNameReclaim); ok && poolInfo != nil {
			for numaID, cpuset := range poolInfo.TopologyAwareAssignments {
				if !cpuset.IsEmpty() {
					liveReclaimByNUMA[numaID] = cpuset.Size()
				}
			}
		}
	}

	// Build a value-semantic, immutable snapshot of the advisor's mutable bookkeeping
	// for this cycle. NewSnapshot deep-copies every map and set, so the assembler
	// reads no advisor-live state and a later mutation of cra.* cannot perturb a cycle
	// the assembler already ran.
	snapshot := provisionassembler.NewSnapshot(
		cra.regionMap,
		cra.reservedForReclaim,
		cra.rampUpReclaimCPUSetCap,
		cra.numaAvailable,
		cra.nonBindingNumas,
		// Single per-cycle injection of the resolved overlap flags straight from
		// dynamicConf. They are snapshotted by value here; ProvisionContext promotes
		// them one-way via the embedded Snapshot, and the assembler reads ctx only.
		dynamicConf.AllowSharedCoresOverlapReclaimedCores,
		dynamicConf.DisableDedicatedCoresOverlapReclaimedCores,
		sortedRampUpDomains(rampUpDomains),
		cra.rampUpDomainTargets,
	)

	return cra.provisionAssembler.AssembleProvision(provisionassembler.ProvisionContext{
		DynamicConfiguration: dynamicConf,
		ReclaimConstraint:    reclaimConstraint,
		ReclaimCeilings:      reclaimCeilings,
		ReclaimActiveScopes:  reclaimActiveScopes,
		LiveReclaimByNUMA:    liveReclaimByNUMA,
		Snapshot:             snapshot,
	})
}

// sortedRampUpDomains converts the cycle-start ramp-up domain set into a stable,
// ordered []int for propagation into InternalCPUCalculationResult. An empty set
// yields nil, which downstream consumers treat as "no active ramp-up domain".
// The order is irrelevant for set-membership gating but keeps the wire result
// deterministic for tests and metrics.
func sortedRampUpDomains(domains sets.Int) []int {
	if domains.Len() == 0 {
		return nil
	}
	out := make([]int, 0, domains.Len())
	for domain := range domains {
		out = append(out, domain)
	}
	sort.Ints(out)
	return out
}

func (cra *cpuResourceAdvisor) emitMetrics(calculationResult types.InternalCPUCalculationResult) {
	// dedicated regions write their pool entries keyed by pod uid rather than a
	// canonical pool name, so commonstate.GetPoolType would misclassify them as
	// share. collect the dedicated pod uids up front so the emitted pool_type
	// label reflects the real dedicated footprint.
	dedicatedPodUIDs := sets.NewString()
	for _, r := range cra.regionMap {
		if r.Type() != configapi.QoSRegionTypeDedicated {
			continue
		}
		for podUID := range r.GetPods() {
			dedicatedPodUIDs.Insert(podUID)
		}
	}
	poolTypeOf := func(poolName string) string {
		if dedicatedPodUIDs.Has(poolName) {
			return commonstate.PoolNameDedicated
		}
		return commonstate.GetPoolType(poolName)
	}

	// emit region indicator related metrics
	for _, r := range cra.regionMap {
		tags := region.GetRegionBasicMetricTags(r)

		_ = cra.emitter.StoreInt64(metricRegionStatus, int64(cra.period.Seconds()), metrics.MetricTypeNameCount, tags...)

		indicators := r.GetControlEssentials().Indicators
		for indicatorName, indicator := range indicators {
			_ = cra.emitter.StoreFloat64(metricRegionIndicatorTargetPrefix+indicatorName, indicator.Target, metrics.MetricTypeNameRaw, tags...)
			_ = cra.emitter.StoreFloat64(metricRegionIndicatorCurrentPrefix+indicatorName, indicator.Current, metrics.MetricTypeNameRaw, tags...)
			_ = cra.emitter.StoreFloat64(metricRegionIndicatorErrorPrefix+indicatorName, indicator.Current-indicator.Target, metrics.MetricTypeNameRaw, tags...)
		}
	}

	// emit calculated pool sizes
	for poolName, poolEntry := range calculationResult.PoolEntries {
		for numaID, cpuResource := range poolEntry {
			_ = cra.emitter.StoreInt64(metricCPUAdvisorPoolSize, int64(cpuResource.Size), metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "name", Val: poolName},
				metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
				metrics.MetricTag{Key: "pool_type", Val: poolTypeOf(poolName)},
				metrics.MetricTag{Key: "overlap", Val: "none"})
			_ = cra.emitter.StoreFloat64(metricCPUAdvisorPoolQuota, cpuResource.Quota, metrics.MetricTypeNameRaw,
				metrics.MetricTag{Key: "name", Val: poolName},
				metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
				metrics.MetricTag{Key: "pool_type", Val: poolTypeOf(poolName)})
		}
	}

	for poolName, overlapInfo := range calculationResult.PoolOverlapInfo {
		for numaID, poolOverlapInfo := range overlapInfo {
			for target, overlap := range poolOverlapInfo {
				_ = cra.emitter.StoreInt64(metricCPUAdvisorPoolSize, int64(overlap), metrics.MetricTypeNameRaw,
					metrics.MetricTag{Key: "name", Val: poolName},
					metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
					metrics.MetricTag{Key: "pool_type", Val: poolTypeOf(poolName)},
					metrics.MetricTag{Key: "overlap", Val: target})
			}
		}
	}

	for poolName, overlapInfo := range calculationResult.PoolOverlapPodContainerInfo {
		for numaID, poolOverlapInfo := range overlapInfo {
			for podUID, overlapContainer := range poolOverlapInfo {
				// todo: current only emit first container overlap, because other containers' overlap is same now
				for _, overlap := range overlapContainer {
					_ = cra.emitter.StoreInt64(metricCPUAdvisorPoolSize, int64(overlap), metrics.MetricTypeNameRaw,
						metrics.MetricTag{Key: "name", Val: poolName},
						metrics.MetricTag{Key: "numa_id", Val: strconv.Itoa(numaID)},
						metrics.MetricTag{Key: "pool_type", Val: poolTypeOf(poolName)},
						metrics.MetricTag{Key: "overlap", Val: podUID})
					break
				}
			}
		}
	}

	cra.emitReclaimScopeDiagnostics()

	emitDefaultShareBackfillMetrics(cra.emitter, calculationResult.DefaultShareBackfill)
}

// emitReclaimScopeDiagnostics reports the per-scope reclaim constraint guard
// state (rate-limited ceiling, desired, floor, steady cap, phase) as gauges. It
// deliberately does NOT replace cpu_advisor_pool_size, which stays the published
// quantity the assembler lands.
func (cra *cpuResourceAdvisor) emitReclaimScopeDiagnostics() {
	for _, d := range cra.reclaimConstraintGuard.snapshot() {
		tags := []metrics.MetricTag{{Key: "scope", Val: d.Scope}}
		_ = cra.emitter.StoreInt64(metricReclaimScopeCeiling, d.Ceiling, metrics.MetricTypeNameRaw, tags...)
		_ = cra.emitter.StoreInt64(metricReclaimScopeDesired, d.Desired, metrics.MetricTypeNameRaw, tags...)
		_ = cra.emitter.StoreInt64(metricReclaimScopeFloor, d.Floor, metrics.MetricTypeNameRaw, tags...)
		_ = cra.emitter.StoreInt64(metricReclaimScopeSteadyCap, d.SteadyCap, metrics.MetricTypeNameRaw, tags...)
		_ = cra.emitter.StoreInt64(metricReclaimScopeLastPublished, d.LastPublished, metrics.MetricTypeNameRaw, tags...)
		// ACK health: unacked_cycles gauge, acked 0/1 gauge, and a cumulative
		// ack_stalled counter so canary can tell whether the ACK loop is stuck.
		_ = cra.emitter.StoreInt64(metricReclaimScopeUnackedCycles, d.UnackedCycles, metrics.MetricTypeNameRaw, tags...)
		_ = cra.emitter.StoreInt64(metricReclaimScopeAcked, d.Acked, metrics.MetricTypeNameRaw, tags...)
		_ = cra.emitter.StoreInt64(metricReclaimScopeAckStalled, d.StallCycles, metrics.MetricTypeNameCount, tags...)
		// The active phase is reported purely as a tag on a presence gauge
		// (value always 1): encoding the same information both as a numeric
		// code and as a tag would duplicate it and drift on renames.
		_ = cra.emitter.StoreInt64(metricReclaimScopePhase, 1, metrics.MetricTypeNameRaw,
			append(tags, metrics.MetricTag{Key: "phase", Val: d.Phase})...)
	}
}

// emitDefaultShareBackfillMetrics reports the structured diagnostics of the
// default share upper-bound backfill as raw int64 metrics. It is a no-op when
// the backfill feature is disabled so that clusters without the feature do not
// emit misleading zero-valued series.
func emitDefaultShareBackfillMetrics(emitter metrics.MetricEmitter, diagnostics types.DefaultShareBackfillDiagnostics) {
	if !diagnostics.Enabled {
		return
	}
	values := map[string]int{
		metricCPUAdvisorMachineAllocatable:       diagnostics.AllocatableBudget,
		metricCPUAdvisorReserveSize:              diagnostics.ReserveSize,
		metricCPUAdvisorFixedPoolSize:            diagnostics.FixedPoolSize,
		metricCPUAdvisorRawReclaimSize:           diagnostics.RawReclaimSize,
		metricCPUAdvisorFinalReclaimSize:         diagnostics.FinalReclaimSize,
		metricCPUAdvisorReclaimClampedSize:       diagnostics.ReleasedReclaimSize,
		metricCPUAdvisorDedicatedSize:            diagnostics.DedicatedSize,
		metricCPUAdvisorIsolationSize:            diagnostics.IsolationSize,
		metricCPUAdvisorCustomSharedSize:         diagnostics.CustomSharedSize,
		metricCPUAdvisorSNBSize:                  diagnostics.SNBSize,
		metricCPUAdvisorExclusiveNUMASize:        diagnostics.ExclusiveNUMASize,
		metricCPUAdvisorPinnedCPUSize:            diagnostics.PinnedCPUSize,
		metricCPUAdvisorDefaultShareBefore:       diagnostics.DefaultShareBeforeBackfill,
		metricCPUAdvisorDefaultShareBackfilled:   diagnostics.DefaultShareBackfilled,
		metricCPUAdvisorDefaultShareFinal:        diagnostics.DefaultShareFinal,
		metricCPUAdvisorUnassignedNonReclaimSize: diagnostics.UnassignedNonReclaimSize,
	}
	for name, value := range values {
		_ = emitter.StoreInt64(name, int64(value), metrics.MetricTypeNameRaw)
	}
}
