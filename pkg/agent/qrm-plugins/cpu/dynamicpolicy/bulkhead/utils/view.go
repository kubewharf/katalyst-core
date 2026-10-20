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

package utils

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/bulkhead/model"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/calculator"
	cpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	"github.com/kubewharf/katalyst-core/pkg/config"
	dynamicconfig "github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepackage "github.com/kubewharf/katalyst-core/pkg/util/resource-package"
)

type CPUSetPartitionViewOptions struct {
	NonReclaimPoolMinSize             int64
	HardPartitionEnabled              bool
	HardPartitionReclaimTargetPerNUMA map[int]int
	HardPartitionTargetError          error
	ReserveCPUReversely               bool
	TransientProtectedNonReclaim      machine.CPUSet
}

type CPUSetPartitionViewState struct {
	State                         cpustate.ReadonlyState
	ReservedCPUs                  machine.CPUSet
	ReservedReclaimedCPUs         machine.CPUSet
	ReservedReclaimedCPUsFallback int
}

// RampUpAffectedNUMAs returns the real NUMA ids that must receive a hard
// reclaim floor when the given ramp-up domains are active. It must stay
// lockstep with dynamicpolicy.affectedRampUpNUMAs:
//
//   - immutable-per-NUMA mode (dedicated/reclaim disjoint): only real NUMA
//     domains affect their own NUMA; the global domain affects no real NUMA.
//   - overlap mode: the global domain (FakedNUMAID) propagates to every real
//     NUMA; real NUMA domains affect only their own NUMA.
func RampUpAffectedNUMAs(rampUpDomains sets.Int, topology *machine.CPUTopology, immutablePerNUMA bool) sets.Int {
	affected := sets.NewInt()
	if rampUpDomains == nil || topology == nil {
		return affected
	}
	for _, domain := range rampUpDomains.List() {
		if domain == commonstate.FakedNUMAID {
			if !immutablePerNUMA {
				for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceNoSortInt() {
					affected.Insert(numaID)
				}
			}
			continue
		}
		affected.Insert(domain)
	}
	return affected
}

// scopeHardPartitionReclaimTargets zeroes the reclaim target for every NUMA not
// in affectedNUMAs, but keeps all NUMA keys present so downstream validators do
// not report a missing target.
func scopeHardPartitionReclaimTargets(targets map[int]int, affectedNUMAs sets.Int, topology *machine.CPUTopology) map[int]int {
	if targets == nil {
		return targets
	}
	if affectedNUMAs == nil {
		affectedNUMAs = sets.NewInt()
	}
	scoped := make(map[int]int, len(targets))
	for numaID, target := range targets {
		if affectedNUMAs.Has(numaID) {
			scoped[numaID] = target
		} else {
			scoped[numaID] = 0
		}
	}
	return scoped
}

func NewCPUSetPartitionViewOptions(
	coreConf *config.Configuration,
	dynamicConf *dynamicconfig.Configuration,
	topology *machine.CPUTopology,
	rampUpDomains sets.Int,
) CPUSetPartitionViewOptions {
	opts := newCPUSetPartitionViewOptions(coreConf, dynamicConf, rampUpDomains)
	if !opts.HardPartitionEnabled {
		return opts
	}
	if topology == nil {
		opts.HardPartitionTargetError = fmt.Errorf(
			"resolve bulkhead hard-partition reclaim targets: missing topology")
		return opts
	}

	targets, err := machine.ResolveHardPartitionReclaimTargets(dynamicConf, topology, 0, nil, nil)
	if err != nil {
		opts.HardPartitionTargetError = err
	} else {
		opts.HardPartitionReclaimTargetPerNUMA = scopeHardPartitionReclaimTargets(
			targets, RampUpAffectedNUMAs(rampUpDomains, topology, false), topology)
	}
	return opts
}

func NewCPUSetPartitionViewOptionsWithState(
	coreConf *config.Configuration,
	dynamicConf *dynamicconfig.Configuration,
	topology *machine.CPUTopology,
	viewState CPUSetPartitionViewState,
	rampUpDomains sets.Int,
) CPUSetPartitionViewOptions {
	opts := newCPUSetPartitionViewOptions(coreConf, dynamicConf, rampUpDomains)
	if !opts.HardPartitionEnabled {
		return opts
	}
	if topology == nil {
		opts.HardPartitionTargetError = fmt.Errorf(
			"resolve bulkhead hard-partition reclaim targets: missing topology")
		return opts
	}

	immutablePerNUMA := viewState.State != nil &&
		viewState.State.GetDisableDedicatedCoresOverlapReclaimedCores()
	eligibleByNUMA, err := eligibleCPUSetByNUMA(viewState.State, topology, viewState.ReservedCPUs)
	if err != nil {
		opts.HardPartitionTargetError = err
		return opts
	}
	targets, err := machine.ResolveHardPartitionReclaimTargets(
		dynamicConf,
		topology,
		viewState.ReservedReclaimedCPUsFallback,
		func(numaID int) int {
			return viewState.ReservedReclaimedCPUs.Intersection(eligibleByNUMA[numaID]).Size()
		},
		func(numaID int) int { return eligibleByNUMA[numaID].Size() },
	)
	if err != nil {
		opts.HardPartitionTargetError = err
	} else {
		opts.HardPartitionReclaimTargetPerNUMA = scopeHardPartitionReclaimTargets(
			targets, RampUpAffectedNUMAs(rampUpDomains, topology, immutablePerNUMA), topology)
	}
	return opts
}

func newCPUSetPartitionViewOptions(
	coreConf *config.Configuration,
	dynamicConf *dynamicconfig.Configuration,
	rampUpDomains sets.Int,
) CPUSetPartitionViewOptions {
	nonReclaimPoolMinSize := configuredNonReclaimPoolMinSize(dynamicConf)
	if nonReclaimPoolMinSize <= 0 && coreConf != nil && coreConf.DynamicAgentConfiguration != nil {
		nonReclaimPoolMinSize = configuredNonReclaimPoolMinSize(coreConf.DynamicAgentConfiguration.GetDynamicConfiguration())
	}

	opts := CPUSetPartitionViewOptions{
		NonReclaimPoolMinSize:             nonReclaimPoolMinSize,
		HardPartitionEnabled:              rampUpDomains.Len() > 0 && hardPartitionEnabled(dynamicConf),
		HardPartitionReclaimTargetPerNUMA: map[int]int{},
	}
	if coreConf != nil {
		opts.ReserveCPUReversely = coreConf.EnableReserveCPUReversely
	}
	return opts
}

func eligibleCPUSetByNUMA(
	state cpustate.ReadonlyState,
	topology *machine.CPUTopology,
	reservedCPUs machine.CPUSet,
) (map[int]machine.CPUSet, error) {
	if state == nil {
		return nil, fmt.Errorf("resolve bulkhead hard-partition reclaim targets: missing state")
	}

	machineState := state.GetMachineState()
	eligibleByNUMA := make(map[int]machine.CPUSet, topology.CPUDetails.NUMANodes().Size())
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		numaState := machineState[numaID]
		if numaState == nil {
			return nil, fmt.Errorf(
				"resolve bulkhead hard-partition reclaim targets: missing machine state for NUMA %d",
				numaID,
			)
		}

		numaCPUs := topology.CPUDetails.CPUsInNUMANodes(numaID)
		stateCPUs := numaState.DefaultCPUSet.Union(numaState.AllocatedCPUSet)
		if outside := stateCPUs.Difference(numaCPUs); !outside.IsEmpty() {
			return nil, fmt.Errorf(
				"resolve bulkhead hard-partition reclaim targets: machine state for NUMA %d contains CPUs outside NUMA topology: %s",
				numaID,
				outside.String(),
			)
		}
		if overlap := numaState.DefaultCPUSet.Intersection(numaState.AllocatedCPUSet); !overlap.IsEmpty() {
			return nil, fmt.Errorf(
				"resolve bulkhead hard-partition reclaim targets: machine state for NUMA %d has default/allocated overlap: %s",
				numaID,
				overlap.String(),
			)
		}
		if !stateCPUs.Equals(numaCPUs) {
			return nil, fmt.Errorf(
				"resolve bulkhead hard-partition reclaim targets: machine state for NUMA %d does not cover NUMA topology: state=%s topology=%s",
				numaID,
				stateCPUs.String(),
				numaCPUs.String(),
			)
		}
		eligibleByNUMA[numaID] = numaState.GetAvailableCPUSet(reservedCPUs)
	}
	return eligibleByNUMA, nil
}

func configuredNonReclaimPoolMinSize(conf *dynamicconfig.Configuration) int64 {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return 0
	}
	return conf.AdminQoSConfiguration.CPUPluginConfiguration.BulkheadConfig.NonReclaimPoolMinSize
}

func hardPartitionEnabled(conf *dynamicconfig.Configuration) bool {
	if conf == nil || conf.AdminQoSConfiguration == nil || conf.AdminQoSConfiguration.CPUPluginConfiguration == nil {
		return false
	}
	return conf.EnableReclaim &&
		conf.AdminQoSConfiguration.CPUPluginConfiguration.EnableRampUpReclaimHardPartition
}

func normalizeDefaultSharePlaceholder(
	allowOverlap bool,
	defaultShareBaseline machine.CPUSet,
	sharePool machine.CPUSet,
	sharePoolMap map[string]machine.CPUSet,
	reclaimRaw machine.CPUSet,
	dedicated machine.CPUSet,
	isolation machine.CPUSet,
	sharedRampUp machine.CPUSet,
	sharedRampUpByPool map[string]machine.CPUSet,
) (machine.CPUSet, map[string]machine.CPUSet, bool) {
	normalizedMap := make(map[string]machine.CPUSet, len(sharePoolMap))
	for poolName, cpus := range sharePoolMap {
		normalizedMap[poolName] = cpus.Clone()
	}

	// The full non-reserved share set is the reset placeholder until reclaim is
	// provisioned. Once a non-overlap reclaim budget exists, keeping that placeholder
	// would make the structural reclaim complement empty. Normalize the placeholder
	// independently of hard-partition mode, while preserving explicit shared ramp-up
	// ownership (the RDT/declared-pool bookkeeping for active SNB ramp-up CPUs).
	if allowOverlap || reclaimRaw.IsEmpty() || !sharePool.Equals(defaultShareBaseline) {
		return sharePool.Clone(), normalizedMap, false
	}

	fixedNonShare := reclaimRaw.Union(dedicated).Union(isolation)
	normalizedShare := sharePool.Difference(fixedNonShare).Union(sharedRampUp)
	for poolName, cpus := range normalizedMap {
		normalizedMap[poolName] = cpus.Difference(fixedNonShare)
	}
	for poolName, cpus := range sharedRampUpByPool {
		normalizedMap[poolName] = normalizedMap[poolName].Union(cpus)
	}
	return normalizedShare, normalizedMap, true
}

func BuildCPUSetPartitionView(state cpustate.ReadonlyState, topology *machine.CPUTopology, opts CPUSetPartitionViewOptions) (*model.DesiredView, map[string]map[string]struct{}) {
	view := model.NewDesiredView()
	if state == nil || topology == nil {
		return view, map[string]map[string]struct{}{}
	}

	allowOverlap := state.GetAllowSharedCoresOverlapReclaimedCores()
	podEntries := state.GetPodEntries()
	poolTypeOf := func(poolName string) string {
		return commonstate.GetPoolType(commonstate.OwnerPoolNameTranslator.Translate(poolName))
	}
	recordContainerCPUSet := func(podUID, containerName string, cpus machine.CPUSet) {
		if _, ok := view.ContainerCPUSetByPod[podUID]; !ok {
			view.ContainerCPUSetByPod[podUID] = map[string]machine.CPUSet{}
		}
		view.ContainerCPUSetByPod[podUID][containerName] = cpus.Clone()
	}
	sharedPoolNameForAllocation := func(allocation *cpustate.AllocationInfo) string {
		poolName, err := allocation.GetSpecifiedNUMABindingPoolName()
		if err != nil {
			poolName = commonstate.PoolNameShare
		}
		return resourcepackage.WrapOwnerPoolName(poolName, allocation.GetResourcePackageName())
	}
	sharedRampUp := machine.NewCPUSet()
	sharedRampUpByPool := map[string]machine.CPUSet{}
	// activeSNBProtected collects the exact CPUs held by already-active (committed)
	// shared-NUMA-binding ramp-up containers. SysAdvisor's reclaim budget is computed
	// without node topology awareness, so it can claim CPUs that such a container has
	// already been pinned to. Rather than folding these CPUs into the share pool (which
	// would shrink DesiredReclaimEffective below the SysAdvisor target), they are
	// protected transiently on the physical reclaim side via
	// ApplyTransientProtectedNonReclaim: DesiredReclaimEffective stays the SysAdvisor
	// target and ReclaimEffective becomes the physically enforced, transient-trimmed
	// set. This is rebuilt from the same ReadonlyState snapshot every revision, is never
	// recorded as a pool owner, and carries no cache/generation/TTL.
	activeSNBProtected := machine.NewCPUSet()
	// The pending shared-non-reclaim projection mode is only valid when the
	// prepare snapshot is neither hard-partitioned nor in share/reclaim overlap:
	// in those modes the share pool is already structurally disjoint from (or
	// deliberately overlapping with) reclaim, so folding a pending allocation onto
	// the shared domain must not change the partition. Outside this gate every
	// pending protection keeps its exact (whole-core) behavior.
	// Hard partition no longer disqualifies a pending non-SNB shared ramp-up
	// container from projection: the active ramp-up reclaim floor must coexist with
	// projecting pending shared pods onto the validated non-reclaim domain. Only
	// share/reclaim overlap does.
	projectionEligibleSnapshot := !allowOverlap
	// pendingSharedProjection is a transient, per-container planning hint (NOT a
	// business partition field). It is returned alongside the view so the topology
	// plugin can project an eligible pending shared ramp-up container onto the
	// shared non-reclaim domain. It deliberately never lives on DesiredView: it
	// must not participate in DeepCopy, view equality, revision, or checkpoint.
	pendingSharedProjection := map[string]map[string]struct{}{}

	for _, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocation := range containerEntries {
			if allocation != nil {
				recordContainerCPUSet(allocation.PodUid, allocation.ContainerName, allocation.AllocationResult)
				if projectionEligibleSnapshot &&
					allocation.CheckShared() &&
					allocation.RampUp &&
					!allocation.CheckSharedNUMABinding() {
					// Eligible pending shared ramp-up (non-SNB) for THIS container
					// only: classified per allocation, never OR-ed across a pod's
					// other containers. The pending builder looks it up exactly by
					// (podUID, containerName).
					if pendingSharedProjection[allocation.PodUid] == nil {
						pendingSharedProjection[allocation.PodUid] = map[string]struct{}{}
					}
					pendingSharedProjection[allocation.PodUid][allocation.ContainerName] = struct{}{}
				}
				if !allocation.AllocationResult.IsEmpty() &&
					allocation.RampUp &&
					allocation.CheckShared() &&
					allocation.CheckSharedNUMABinding() &&
					!allocation.CheckReclaimed() {
					// Active SNB ramp-up containers hold exact CPUs. They belong to a
					// declared share-NUMA pool, so keep that RDT/owner bookkeeping in the
					// share pool map. But SysAdvisor's reclaim budget can still claim these
					// CPUs: rather than letting that fold shrink DesiredReclaimEffective below
					// the SysAdvisor target, the exact CPUs are collected as activeSNBProtected
					// and excluded from the reclaim derive, then protected transiently on the
					// physical reclaim side via ApplyTransientProtectedNonReclaim. This is
					// rebuilt from the same ReadonlyState snapshot every revision and carries no
					// cache/generation/TTL.
					view.SharePool = view.SharePool.Union(allocation.AllocationResult)
					sharedRampUp = sharedRampUp.Union(allocation.AllocationResult)
					poolName := sharedPoolNameForAllocation(allocation)
					sharedRampUpByPool[poolName] = sharedRampUpByPool[poolName].Union(allocation.AllocationResult)
					activeSNBProtected = activeSNBProtected.Union(allocation.AllocationResult)
				}
			}
		}
	}

	for poolName, containerEntries := range podEntries {
		if !containerEntries.IsPoolEntry() {
			continue
		}
		entry := containerEntries[commonstate.FakedContainerName]
		if entry == nil {
			continue
		}
		switch poolTypeOf(poolName) {
		case commonstate.PoolNameReserve:
			view.Reserve = view.Reserve.Union(entry.AllocationResult)
		case commonstate.PoolNameShare:
			view.SharePool = view.SharePool.Union(entry.AllocationResult)
			view.SharePoolMap[poolName] = entry.AllocationResult.Clone()
		case commonstate.PoolNamePrefixIsolation:
			view.Isolation = view.Isolation.Union(entry.AllocationResult)
		}
	}
	for poolName, cpus := range sharedRampUpByPool {
		view.SharePoolMap[poolName] = view.SharePoolMap[poolName].Union(cpus)
	}

	for _, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocation := range containerEntries {
			if allocation != nil && allocation.CheckDedicated() {
				view.Dedicated = view.Dedicated.Union(allocation.AllocationResult)
			}
		}
	}

	if reclaimEntries, ok := podEntries[commonstate.PoolNameReclaim]; ok {
		if entry := reclaimEntries[commonstate.FakedContainerName]; entry != nil {
			view.ReclaimRaw = entry.AllocationResult.Clone()
		}
	}
	if allowOverlap && !view.ReclaimRaw.IsEmpty() {
		view.SharePool = view.SharePool.Difference(view.ReclaimRaw)
		for poolName, cpus := range view.SharePoolMap {
			view.SharePoolMap[poolName] = cpus.Difference(view.ReclaimRaw)
		}
	}
	defaultShareBaseline := topology.CPUDetails.CPUs().Difference(view.Reserve)
	shareBeforeNormalization := view.SharePool.Clone()
	var normalized bool
	view.SharePool, view.SharePoolMap, normalized = normalizeDefaultSharePlaceholder(
		allowOverlap,
		defaultShareBaseline,
		view.SharePool,
		view.SharePoolMap,
		view.ReclaimRaw,
		view.Dedicated,
		view.Isolation,
		sharedRampUp,
		sharedRampUpByPool,
	)
	// Active SNB ramp-up CPUs are kept in the share pool for RDT bookkeeping, but they
	// must not shrink the SysAdvisor reclaim target. Excluding them here makes
	// DesiredReclaimEffective equal the raw reclaim budget; the physical
	// NonReclaimPool/ReclaimEffective are transient-trimmed to reclaim them away via
	// ApplyTransientProtectedNonReclaim below.
	view.NonReclaimPool = view.SharePool.Difference(activeSNBProtected).Union(view.Dedicated).Union(view.Isolation)
	if allowOverlap {
		view.ReclaimEffective = view.ReclaimRaw.Clone()
	} else {
		// ReclaimRaw is the affinity budget produced by SysAdvisor after applying
		// reclaimed-cpu-max-ratio. QRM must never widen it when deriving the
		// cgroup target: every CPU outside the effective reclaim set belongs to
		// the non-reclaim domain instead. The effective reclaim set is therefore
		// derived in three steps, each of which can only shrink it:
		//   1. start from all machine CPUs minus the non-reclaim pool and reserve
		//      (the CPUs structurally available to reclaim);
		//   2. intersect with ReclaimRaw so we never exceed SysAdvisor's budget;
		//   3. recompute NonReclaimPool as the complement so the two domains stay
		//      mutually exclusive and jointly cover every non-reserved CPU.
		view.ReclaimEffective = topology.CPUDetails.CPUs().Difference(view.NonReclaimPool).Difference(view.Reserve)
		view.ReclaimEffective = view.ReclaimEffective.Intersection(view.ReclaimRaw)
		if !view.ReclaimRaw.IsEmpty() && view.ReclaimEffective.IsEmpty() {
			general.Warningf(
				"bulkhead: reclaim_effective_collapsed raw=%s share=%s dedicated=%s isolation=%s reserve=%s placeholder_normalized=%t",
				view.ReclaimRaw.String(), view.SharePool.String(), view.Dedicated.String(),
				view.Isolation.String(), view.Reserve.String(), normalized)
		}
		view.NonReclaimPool = topology.CPUDetails.CPUs().Difference(view.ReclaimEffective).Difference(view.Reserve)
		// Step 3 may leave CPUs in NonReclaimPool that no share/dedicated/isolation
		// pool claims (e.g. reclaim was clamped below its structural budget). When
		// the share pool is otherwise empty, seed it with these spare CPUs so they
		// are not left unassigned; this only fills a gap and never overrides an
		// existing share allocation.
		if view.SharePool.IsEmpty() {
			spareShare := view.NonReclaimPool.Difference(view.Dedicated).Difference(view.Isolation)
			if !spareShare.IsEmpty() {
				view.SharePool = spareShare
				view.SharePoolMap[commonstate.PoolNameShare] = spareShare.Clone()
			}
		}
		// Hard-partition reclaim is already materialized and validated by QRM.
		// Bulkhead only projects that ownership; it must not trim legal reclaim
		// to satisfy its independent non-reclaim padding preference.
		if !opts.HardPartitionEnabled {
			padNonReclaimPoolToMinSize(view, topology, opts)
		}
	}
	view.DesiredNonReclaimPool = view.NonReclaimPool.Clone()
	view.DesiredReclaimEffective = view.ReclaimEffective.Clone()
	rebuildDesiredReclaimEffectivePerNUMA(view, topology)
	// The active-SNB ramp-up protection is folded into the transient protected set ONLY
	// when share/reclaim overlap is off: in overlap mode share deliberately overlaps
	// reclaim, so a transient non-reclaim protection would contradict that contract.
	// ApplyTransientProtectedNonReclaim leaves DesiredReclaimEffective untouched and only
	// trims the physical ReclaimEffective, so the Desired-vs-physical split holds.
	protected := opts.TransientProtectedNonReclaim
	if !allowOverlap {
		protected = protected.Union(activeSNBProtected)
	}
	ApplyTransientProtectedNonReclaim(view, topology, protected)
	rebuildReclaimEffectivePerNUMA(view, topology)
	if normalized {
		general.InfofV(4,
			"bulkhead: admission partition view normalized default share placeholder, share_before=%s share_after=%s reclaim_raw=%s desired_reclaim=%s applied_reclaim=%s dedicated=%s isolation=%s reserve=%s hard_partition=%t overlap=%t",
			shareBeforeNormalization.String(), view.SharePool.String(), view.ReclaimRaw.String(),
			view.DesiredReclaimEffective.String(), view.ReclaimEffective.String(), view.Dedicated.String(),
			view.Isolation.String(), view.Reserve.String(), opts.HardPartitionEnabled, allowOverlap)
	}
	rebuildDesiredPoolOwners(view, podEntries)
	return view, pendingSharedProjection
}

func rebuildDesiredPoolOwners(view *model.DesiredView, podEntries cpustate.PodEntries) {
	view.PoolOwners = map[model.CPUSetPoolIdentity]model.DesiredPoolOwner{}
	recordDesiredPoolOwner(
		view.PoolOwners,
		model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindReclaim},
		"",
		"",
		view.ReclaimEffective,
	)
	for poolName, cpus := range view.SharePoolMap {
		recordDesiredPoolOwner(
			view.PoolOwners,
			model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: poolName},
			"",
			"",
			cpus,
		)
	}

	for _, containerEntries := range podEntries {
		if containerEntries.IsPoolEntry() {
			continue
		}
		for _, allocation := range containerEntries {
			identity, ok := LeafPoolIdentityForAllocation(allocation)
			if !ok || identity.Kind == model.CPUSetPoolKindShare {
				// Share ownership is recorded once from SharePoolMap above. Per-leaf
				// union would change the pool projection and duplicate its authority.
				continue
			}
			recordDesiredPoolOwner(
				view.PoolOwners,
				identity,
				allocation.PodUid,
				allocation.ContainerName,
				allocation.AllocationResult,
			)
		}
	}
}

// LeafPoolIdentityForAllocation returns the canonical owner key used by
// DesiredView.PoolOwners for a concrete container allocation. Share keys retain
// resource-package and NUMA suffixes because SharePoolMap uses the exact pool
// entry name; only pool type classification uses OwnerPoolNameTranslator.
func LeafPoolIdentityForAllocation(allocation *cpustate.AllocationInfo) (model.CPUSetPoolIdentity, bool) {
	if allocation == nil {
		return model.CPUSetPoolIdentity{}, false
	}
	if allocation.RampUp && allocation.CheckSharedNUMABinding() {
		return model.CPUSetPoolIdentity{}, false
	}

	poolType := commonstate.GetPoolType(
		commonstate.OwnerPoolNameTranslator.Translate(allocation.OwnerPoolName))
	if poolType == commonstate.PoolNamePrefixIsolation {
		return model.CPUSetPoolIdentity{
			Kind:         model.CPUSetPoolKindIsolation,
			PodNamespace: allocation.PodNamespace,
			PodName:      allocation.PodName,
		}, true
	}
	switch poolType {
	case commonstate.PoolNameReserve, commonstate.PoolNamePrefixSystem, commonstate.PoolNameReclaim:
		return model.CPUSetPoolIdentity{}, false
	}
	if allocation.CheckDedicated() {
		return model.CPUSetPoolIdentity{
			Kind:         model.CPUSetPoolKindDedicated,
			PodNamespace: allocation.PodNamespace,
			PodName:      allocation.PodName,
		}, true
	}
	ownerPoolName := allocation.OwnerPoolName
	if ownerPoolName == "" {
		ownerPoolName = commonstate.PoolNameShare
	}
	return model.CPUSetPoolIdentity{Kind: model.CPUSetPoolKindShare, Name: ownerPoolName}, true
}

func recordDesiredPoolOwner(
	owners map[model.CPUSetPoolIdentity]model.DesiredPoolOwner,
	identity model.CPUSetPoolIdentity,
	podUID string,
	containerName string,
	cpus machine.CPUSet,
) {
	owner := owners[identity]
	if owner.ProofPodUID == "" {
		owner.ProofPodUID = podUID
	}
	owner.ExpectedCPUSet = owner.ExpectedCPUSet.Union(cpus)
	if containerName != "" {
		if owner.ContainerCPUSetByName == nil {
			owner.ContainerCPUSetByName = map[string]machine.CPUSet{}
		}
		owner.ContainerCPUSetByName[containerName] = owner.ContainerCPUSetByName[containerName].Union(cpus)
	}
	owners[identity] = owner
}

func BuildValidatedCPUSetPartitionView(state cpustate.ReadonlyState, topology *machine.CPUTopology, opts CPUSetPartitionViewOptions) (*model.DesiredView, map[string]map[string]struct{}, error) {
	if opts.HardPartitionTargetError != nil {
		return nil, nil, opts.HardPartitionTargetError
	}
	if opts.HardPartitionEnabled && state == nil {
		return nil, nil, fmt.Errorf("build bulkhead hard-partition view: missing state")
	}
	view, pendingSharedProjection := BuildCPUSetPartitionView(state, topology, opts)
	if err := ValidateCPUSetPartitionView(view, topology); err != nil {
		return nil, nil, err
	}
	if opts.HardPartitionEnabled {
		// NUMAs owned by a committed steady exclusive DNB keep only their
		// finalized reserve once ramp-up ends, regardless of reclaimability; the
		// precommit validator must skip them via the shared state helper,
		// otherwise it rejects every other ramp-up QoS.
		skipNUMAs := state.GetPodEntries().SteadyExclusiveNUMAs(topology)
		if err := validateHardPartitionReclaimPerNUMA(
			view.ReclaimEffectivePerNUMA, opts.HardPartitionReclaimTargetPerNUMA,
			skipNUMAs, topology); err != nil {
			return nil, nil, err
		}
	}
	return view, pendingSharedProjection, nil
}

func validateHardPartitionReclaimPerNUMA(
	reclaimPerNUMA map[int]machine.CPUSet,
	targetPerNUMA map[int]int,
	skipNUMAs sets.Int,
	topology *machine.CPUTopology,
) error {
	if topology == nil {
		return nil
	}

	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		if skipNUMAs.Has(numaID) {
			continue
		}
		target, ok := targetPerNUMA[numaID]
		if !ok {
			return fmt.Errorf("bulkhead hard-partition reclaim target missing for NUMA %d", numaID)
		}
		count := reclaimPerNUMA[numaID].Size()
		if count < target {
			return fmt.Errorf("bulkhead hard-partition reclaim NUMA %d has %d CPUs, target is %d", numaID, count, target)
		}
	}
	return nil
}

// ValidateHardPartitionAppliedView verifies the topology layer's per-NUMA
// reclaim proof without reconstructing it from the aggregate reclaim root.
// Callers must handle any level-specific reset sentinel before invoking this
// common validator; an empty aggregate reclaim set otherwise fails closed.
func ValidateHardPartitionAppliedView(
	view *model.AppliedView,
	state cpustate.ReadonlyState,
	topology *machine.CPUTopology,
	opts CPUSetPartitionViewOptions,
) error {
	if !opts.HardPartitionEnabled || view == nil || topology == nil {
		return nil
	}

	skipNUMAs := sets.NewInt()
	if state != nil {
		skipNUMAs = state.GetPodEntries().SteadyExclusiveNUMAs(topology)
	}
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceInt() {
		if skipNUMAs.Has(numaID) {
			continue
		}
		if _, ok := view.ReclaimEffectivePerNUMA[numaID]; !ok {
			return fmt.Errorf("bulkhead hard-partition applied reclaim proof missing for NUMA %d", numaID)
		}
	}
	return validateHardPartitionReclaimPerNUMA(
		view.ReclaimEffectivePerNUMA,
		opts.HardPartitionReclaimTargetPerNUMA,
		skipNUMAs,
		topology,
	)
}

// BuildCPUSetPartitionViewFromTarget creates an owned partition view whose
// reclaim domain is the topology layer's write-verified target. Metadata that
// is independent of partition ownership is copied from desired.
func BuildCPUSetPartitionViewFromTarget(
	desired *model.DesiredView,
	reclaimTarget machine.CPUSet,
	topology *machine.CPUTopology,
) *model.CPUSetPartitionView {
	view := model.NewCPUSetPartitionView()
	if desired != nil {
		view = *desired.CPUSetPartitionView.DeepCopy()
	}

	view.ReclaimEffective = reclaimTarget.Clone()
	view.ReclaimEffectivePerNUMA = map[int]machine.CPUSet{}
	if topology == nil {
		view.NonReclaimPool = machine.NewCPUSet()
		return &view
	}

	available := topology.CPUDetails.CPUs().Difference(view.Reserve)
	view.NonReclaimPool = available.Difference(view.ReclaimEffective)
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		view.ReclaimEffectivePerNUMA[numaID] = view.ReclaimEffective.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
	}
	return &view
}

func ValidateCPUSetPartitionView(view *model.DesiredView, topology *machine.CPUTopology) error {
	if view == nil || topology == nil {
		return nil
	}
	machineCPUs := topology.CPUDetails.CPUs()
	for name, cpus := range map[string]machine.CPUSet{
		"reserve":                         view.Reserve,
		"share pool":                      view.SharePool,
		"dedicated":                       view.Dedicated,
		"isolation":                       view.Isolation,
		"reclaim raw":                     view.ReclaimRaw,
		"non-reclaim pool":                view.NonReclaimPool,
		"reclaim effective":               view.ReclaimEffective,
		"desired non-reclaim pool":        view.DesiredNonReclaimPool,
		"desired reclaim effective":       view.DesiredReclaimEffective,
		"transient protected non-reclaim": view.TransientProtectedNonReclaim,
	} {
		if outside := cpus.Difference(machineCPUs); !outside.IsEmpty() {
			return fmt.Errorf("bulkhead cpuset partition %s has CPUs outside machine topology: %s", name, outside.String())
		}
	}
	if overlap := view.NonReclaimPool.Intersection(view.ReclaimEffective); !overlap.IsEmpty() {
		return fmt.Errorf("bulkhead cpuset partition has non-reclaim/reclaim overlap: %s", overlap.String())
	}
	if err := validateExactNUMAProjection(
		"reclaim effective", view.ReclaimEffective, view.ReclaimEffectivePerNUMA, topology); err != nil {
		return err
	}
	if err := validateExactNUMAProjection(
		"desired reclaim effective", view.DesiredReclaimEffective, view.DesiredReclaimEffectivePerNUMA, topology); err != nil {
		return err
	}
	return nil
}

func validateExactNUMAProjection(
	name string,
	global machine.CPUSet,
	perNUMA map[int]machine.CPUSet,
	topology *machine.CPUTopology,
) error {
	numaIDs := topology.CPUDetails.NUMANodes()
	if len(perNUMA) != numaIDs.Size() {
		return fmt.Errorf("bulkhead cpuset partition %s NUMA bucket count %d does not match machine NUMA count %d",
			name, len(perNUMA), numaIDs.Size())
	}
	for numaID := range perNUMA {
		if !numaIDs.Contains(numaID) {
			return fmt.Errorf("bulkhead cpuset partition %s has unknown NUMA bucket: %d", name, numaID)
		}
	}
	for _, numaID := range numaIDs.ToSliceInt() {
		actual, ok := perNUMA[numaID]
		if !ok {
			return fmt.Errorf("bulkhead cpuset partition missing %s NUMA bucket: %d", name, numaID)
		}
		expected := global.Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		if !actual.Equals(expected) {
			return fmt.Errorf("bulkhead cpuset partition %s NUMA bucket %d is %s, want exact global projection %s",
				name, numaID, actual.String(), expected.String())
		}
	}
	return nil
}

func ApplyTransientProtectedNonReclaim(view *model.DesiredView, topology *machine.CPUTopology, protected machine.CPUSet) {
	if view == nil || topology == nil {
		return
	}
	if protected.IsEmpty() {
		view.TransientProtectedNonReclaim = machine.NewCPUSet()
		rebuildTransientProtectedNonReclaimPerNUMA(view, topology)
		return
	}
	available := topology.CPUDetails.CPUs().Difference(view.Reserve)
	view.TransientProtectedNonReclaim = protected.Intersection(available)
	view.NonReclaimPool = view.DesiredNonReclaimPool.Union(view.TransientProtectedNonReclaim).Intersection(available)
	view.ReclaimEffective = view.DesiredReclaimEffective.Difference(view.NonReclaimPool)
	rebuildReclaimEffectivePerNUMA(view, topology)
	rebuildTransientProtectedNonReclaimPerNUMA(view, topology)
}

func padNonReclaimPoolToMinSize(view *model.DesiredView, topology *machine.CPUTopology, opts CPUSetPartitionViewOptions) {
	if view == nil || topology == nil || opts.NonReclaimPoolMinSize <= 0 {
		return
	}
	currentSize := view.NonReclaimPool.Size()
	if currentSize >= int(opts.NonReclaimPoolMinSize) {
		return
	}
	deficit := int(opts.NonReclaimPoolMinSize) - currentSize
	candidates := view.ReclaimEffective.Clone()
	if candidates.IsEmpty() {
		return
	}
	if deficit > candidates.Size() {
		deficit = candidates.Size()
	}

	padding := calculator.TakeHTByNUMABalanceWithSeed(topology, candidates, view.NonReclaimPool, deficit, opts.ReserveCPUReversely)
	view.NonReclaimPool = view.NonReclaimPool.Union(padding)
	view.ReclaimEffective = view.ReclaimEffective.Difference(padding)
}

// rebuildPerNUMA projects a CPUSet field onto per-NUMA intersections.
// Shared by all *PerNUMA rebuilders to avoid divergent iteration order.
func rebuildPerNUMA(
	view *model.DesiredView,
	topology *machine.CPUTopology,
	source func(*model.DesiredView) machine.CPUSet,
	target func(*model.DesiredView) *map[int]machine.CPUSet,
) {
	if view == nil {
		return
	}
	*target(view) = map[int]machine.CPUSet{}
	if topology == nil {
		return
	}
	for _, numaID := range topology.CPUDetails.NUMANodes().ToSliceNoSortInt() {
		intersection := source(view).Intersection(topology.CPUDetails.CPUsInNUMANodes(numaID))
		(*target(view))[numaID] = intersection
	}
}

func rebuildReclaimEffectivePerNUMA(view *model.DesiredView, topology *machine.CPUTopology) {
	rebuildPerNUMA(view, topology,
		func(v *model.DesiredView) machine.CPUSet { return v.ReclaimEffective },
		func(v *model.DesiredView) *map[int]machine.CPUSet { return &v.ReclaimEffectivePerNUMA })
}

func rebuildDesiredReclaimEffectivePerNUMA(view *model.DesiredView, topology *machine.CPUTopology) {
	rebuildPerNUMA(view, topology,
		func(v *model.DesiredView) machine.CPUSet { return v.DesiredReclaimEffective },
		func(v *model.DesiredView) *map[int]machine.CPUSet { return &v.DesiredReclaimEffectivePerNUMA })
}

func rebuildTransientProtectedNonReclaimPerNUMA(view *model.DesiredView, topology *machine.CPUTopology) {
	rebuildPerNUMA(view, topology,
		func(v *model.DesiredView) machine.CPUSet { return v.TransientProtectedNonReclaim },
		func(v *model.DesiredView) *map[int]machine.CPUSet { return &v.TransientProtectedNonReclaimPerNUMA })
}

// ClonePendingSharedProjection returns a fresh, no-alias copy of a transient
// per-container pending shared projection hint so the outliving override map
// is never mutated by a single adjustment round.
func ClonePendingSharedProjection(in map[string]map[string]struct{}) map[string]map[string]struct{} {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]map[string]struct{}, len(in))
	for podUID, containers := range in {
		if len(containers) == 0 {
			continue
		}
		cloned := make(map[string]struct{}, len(containers))
		for c := range containers {
			cloned[c] = struct{}{}
		}
		out[podUID] = cloned
	}
	return out
}

// UnionPendingSharedProjection returns a fresh no-alias per-pod/container union
// of two transient pending shared projection hints. Either side may be nil. The
// live rebuild is treated as the authoritative superset; the override only adds
// admission-snapshot-only keys. Same-key collisions are idempotent.
func UnionPendingSharedProjection(a, b map[string]map[string]struct{}) map[string]map[string]struct{} {
	if len(a) == 0 {
		return ClonePendingSharedProjection(b)
	}
	out := ClonePendingSharedProjection(a)
	for podUID, containers := range b {
		existing, ok := out[podUID]
		if !ok {
			set := make(map[string]struct{}, len(containers))
			for c := range containers {
				set[c] = struct{}{}
			}
			out[podUID] = set
			continue
		}
		for c := range containers {
			existing[c] = struct{}{}
		}
	}
	return out
}
