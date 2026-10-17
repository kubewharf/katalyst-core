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

package provisionassembler

import (
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/metacache"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/cputypes"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/plugin/qosaware/resource/cpu/region"
	"github.com/kubewharf/katalyst-core/pkg/agent/sysadvisor/types"
	"github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/dynamic"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// ProvisionAssembler assembles internal node provision result.
//
// The assembler is stateless across cycles: the cpu root package hands it a fresh,
// value-semantic ProvisionContext every cycle and the assembler stores nothing
// reachable from it between AssembleProvision calls. Advisor data elements that
// used to be shared with the assembler as live pointers are now deep-copied into
// the embedded Snapshot before the call, so the assembler never reads advisor-live
// maps and a later mutation of the advisor's bookkeeping cannot perturb a cycle it
// already ran.
type ProvisionAssembler interface {
	AssembleProvision(ctx ProvisionContext) (types.InternalCPUCalculationResult, error)
}

// ReclaimConstraint and its None/ReservedFloor enumerators are NOT re-declared
// here: cputypes is the single owner of this type vocabulary, and every
// reference in this package (and in the cpu guard) points at cputypes directly.
// The compatibility alias is intentionally gone so there is exactly one
// definition to reason about. ReclaimConstraintScope remains a thin alias
// because its constructors are ergonomic in-package; the type itself still
// lives in cputypes.

// Snapshot is the value-semantic, immutable per-cycle view of the advisor's
// mutable bookkeeping. The cpu root package builds one every cycle (via
// NewSnapshot) by deep-copying every map and set it owns, so the returned
// Snapshot aliases nothing the advisor can mutate afterwards. The assembler
// treats every field as read-only for the duration of a single AssembleProvision
// call.
//
// Deep-copy boundary (why it is safe): NewSnapshot owns the entire graph it
// returns -- maps, the CPUSet, and each ReclaimConstraintTarget's nested
// Ceiling *int / MemberNUMAs slice are all cloned. The single deliberate
// exception is RegionMap: the region.QoSRegion VALUES are shared read-only.
// That is safe only because the advisor does not mutate a region while the
// assembler is reading it; the map structure (add/remove) is still cloned.
// No field of a Snapshot may be written by the assembler, and no pointer into a
// Snapshot may be retained across AssembleProvision.
type Snapshot struct {
	// RegionMap is map[regionName]region, deep-copied at snapshot time so adding
	// or removing a region on the advisor side does not change the cycle input.
	// Region objects themselves are shared read-only: the assembler only reads
	// them and the advisor does not mutate them during assembly.
	RegionMap map[string]region.QoSRegion
	// ReservedForReclaim is map[numaID]steady reservation floor.
	ReservedForReclaim map[int]int
	// RampUpReclaimCPUSetCap is map[numaID]ramp-up hard target; only NUMAs that
	// themselves host an active ramp-up domain carry a positive entry.
	RampUpReclaimCPUSetCap map[int]int
	// NUMAAvailable is map[numaID]allocatable resource after reserve/forbidden.
	NUMAAvailable map[int]int
	// NonBindingNumas are the NUMAs without a numa-binding workload.
	NonBindingNumas machine.CPUSet
	// RampUpDomains is the sorted per-NUMA domain set that hosts an active ramp-up
	// at cycle start (FakedNUMAID -1 denotes the global/non-binding domain). It is
	// deep-copied into the Snapshot so a later advisor mutation of the live source
	// slice cannot perturb the cycle input; the assembler reads it ctx-side only.
	RampUpDomains []int
	// AllowSharedCoresOverlapReclaimedCores / DisableDedicatedCoresOverlapReclaimedCores
	// are the per-cycle RESOLVED overlap flags, snapshotted (deep-copied by value)
	// from dynamicConf at cycle entry. Snapshot is the immutable sample the
	// calculation chain reads; ProvisionContext promotes these fields one-way to
	// ctx (it embeds this Snapshot) and the assembler never re-reads dynamicConf
	// or a persistent advisor field. bools are value-copied, so there is nothing
	// further to deep-copy.
	AllowSharedCoresOverlapReclaimedCores      bool
	DisableDedicatedCoresOverlapReclaimedCores bool
	// ReclaimDomainTargets carries the per-scope descriptive ramp-up target contract
	// built by the advisor this cycle (Desired, Floor, SteadyCap, optional Ceiling,
	// member NUMAs). It is deep-copied -- including the optional Ceiling *int and the
	// MemberNUMAs slice -- so a later advisor mutation of the live map (or of a
	// target's nested pointer/slice) cannot perturb the cycle input.
	ReclaimDomainTargets map[ReclaimConstraintScope]types.ReclaimConstraintTarget
}

// NewSnapshot deep-copies the advisor's mutable bookkeeping into an immutable,
// value-semantic Snapshot. The caller retains ownership of every passed map and
// CPUSet; mutating them after this call cannot perturb the returned Snapshot. A
// nil source map yields a nil snapshot entry, which the assembler treats as
// "absent for this cycle". The overlap bools are the single per-cycle injection
// point: they come straight from dynamicConf here and nowhere else.
func NewSnapshot(
	regionMap map[string]region.QoSRegion,
	reservedForReclaim, rampUpReclaimCPUSetCap, numaAvailable map[int]int,
	nonBindingNumas machine.CPUSet,
	allowSharedCoresOverlapReclaimedCores, disableDedicatedCoresOverlapReclaimedCores bool,
	rampUpDomains []int,
	reclaimDomainTargets map[ReclaimConstraintScope]types.ReclaimConstraintTarget,
) Snapshot {
	return Snapshot{
		RegionMap:                                  copyRegionMap(regionMap),
		ReservedForReclaim:                         copyIntMap(reservedForReclaim),
		RampUpReclaimCPUSetCap:                     copyIntMap(rampUpReclaimCPUSetCap),
		NUMAAvailable:                              copyIntMap(numaAvailable),
		NonBindingNumas:                            nonBindingNumas.Clone(),
		AllowSharedCoresOverlapReclaimedCores:      allowSharedCoresOverlapReclaimedCores,
		DisableDedicatedCoresOverlapReclaimedCores: disableDedicatedCoresOverlapReclaimedCores,
		RampUpDomains:                              copyIntSlice(rampUpDomains),
		ReclaimDomainTargets:                       copyReclaimDomainTargets(reclaimDomainTargets),
	}
}

// copyIntSlice returns an independent copy of an []int, or nil when the source is
// nil. Values are copied by value, so the result shares no backing storage.
func copyIntSlice(in []int) []int {
	if in == nil {
		return nil
	}
	out := make([]int, len(in))
	copy(out, in)
	return out
}

// copyIntMap returns an independent copy of a map[int]int, or nil when the source
// is nil. Integer values are copied by value, so the result shares no storage with
// the source.
func copyIntMap(in map[int]int) map[int]int {
	if in == nil {
		return nil
	}
	out := make(map[int]int, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// copyRegionMap returns an independent copy of the region map structure. Region
// values are shared read-only (the assembler never mutates them); copying the map
// keys is enough to isolate add/remove of regions on the advisor side.
func copyRegionMap(in map[string]region.QoSRegion) map[string]region.QoSRegion {
	if in == nil {
		return nil
	}
	out := make(map[string]region.QoSRegion, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// copyReclaimDomainTargets returns an independent copy of the advisor's live
// ramp-up target map. The map structure is cloned, and each target's optional
// Ceiling *int and MemberNUMAs []int are cloned as well: the snapshot must own the
// whole graph reachable from it, so a later advisor mutation of either the map or
// a target's nested pointer/slice cannot perturb the cycle input. Integer fields
// are plain value copies.
func copyReclaimDomainTargets(in map[ReclaimConstraintScope]types.ReclaimConstraintTarget) map[ReclaimConstraintScope]types.ReclaimConstraintTarget {
	if in == nil {
		return nil
	}
	out := make(map[ReclaimConstraintScope]types.ReclaimConstraintTarget, len(in))
	for scope, target := range in {
		copied := target
		if target.Ceiling != nil {
			ceiling := *target.Ceiling
			copied.Ceiling = &ceiling
		}
		if target.MemberNUMAs != nil {
			memberNUMAs := make([]int, len(target.MemberNUMAs))
			copy(memberNUMAs, target.MemberNUMAs)
			copied.MemberNUMAs = memberNUMAs
		}
		out[scope] = copied
	}
	return out
}

// ProvisionContext is the immutable, value-semantic per-cycle input handed to
// AssembleProvision. The assembler treats everything reachable through it as
// read-only for the duration of the call and stores none of it between cycles.
// The cpu root package builds a fresh context every cycle.
type ProvisionContext struct {
	DynamicConfiguration *dynamic.Configuration
	ReclaimConstraint    cputypes.ReclaimConstraint
	ReclaimCeilings      map[ReclaimConstraintScope]*int
	// ReclaimActiveScopes identifies the scopes that are constrained this cycle
	// (actively ramped scopes plus scopes still draining back to steadyCap).
	ReclaimActiveScopes map[ReclaimConstraintScope]bool
	// LiveReclaimByNUMA is the QRM-committed reclaim pool cpuset size per NUMA
	// at cycle start. It is a best-effort lower bound: when a real-NUMA ramp-up
	// domain is active and capacity allows, the assembler keeps reclaim at least
	// at this value. Global domain (-1) does NOT broadcast to real NUMAs.
	// CPURequest is a hard guarantee for reclaim-disabled non-exclusive dedicated
	// pools; live continuity MUST NOT reduce dedicated to inflate reclaim.
	LiveReclaimByNUMA map[int]int

	// Snapshot embeds the deep-copied, immutable per-cycle sample: the map/set
	// bookkeeping AND the resolved overlap flags. Because the flags live on
	// Snapshot, ctx.AllowSharedCoresOverlapReclaimedCores / ctx.DisableDedicated
	// resolve (one-way, promoted by embedding) to this cycle's snapshotted values.
	// The assembler reads ctx.* and must never re-read dynamicConf or a persistent
	// advisor field; there is no second copy to fall back to.
	// ReclaimDomainTargets also travels inside the Snapshot (deep-copied).
	Snapshot
}

// InitFunc builds a ProvisionAssembler. The assembler no longer receives the
// advisor's live maps at construction: those arrive per-cycle inside
// ProvisionContext.Snapshot. The initializer only captures long-lived,
// immutable dependencies.
type InitFunc func(conf *config.Configuration, extraConf interface{},
	reader metacache.MetaReader, metaServer *metaserver.MetaServer, emitter metrics.MetricEmitter) ProvisionAssembler

var initializers sync.Map

func RegisterInitializer(name types.CPUProvisionAssemblerName, initFunc InitFunc) {
	initializers.Store(name, initFunc)
}

func GetRegisteredInitializers() map[types.CPUProvisionAssemblerName]InitFunc {
	res := make(map[types.CPUProvisionAssemblerName]InitFunc)
	initializers.Range(func(key, value interface{}) bool {
		res[key.(types.CPUProvisionAssemblerName)] = value.(InitFunc)
		return true
	})
	return res
}
