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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

// rampUpHardPartitionActive reports whether the given NUMA is currently under an
// active ramp-up hard partition. The ramp-up reclaim floor is scoped per NUMA:
// only NUMAs that host an actual ramp-up source carry an entry in the cap map.
// This deliberately replaces the former node-global "cap map non-empty" gate so a
// ramp-up on one NUMA cannot switch hard-partition behavior on another NUMA's
// dedicated pool.
func (pa *ProvisionAssemblerCommon) rampUpHardPartitionActive(ctx ProvisionContext, numaID int) bool {
	// The gate preserves the original "cap map wired" nil semantics: a nil Snapshot
	// map (the field was never populated) means no hard-partition bookkeeping, while
	// an empty-but-present map still engages the global domain below. Do not switch
	// this to len()==0: an empty cap map is the steady-state layout for a global
	// (faked) ramp-up that has no per-NUMA target distribution yet.
	if ctx.RampUpReclaimCPUSetCap == nil {
		return false
	}
	// The global (faked) domain publishes a single aggregate reclaim pool over all
	// non-binding NUMAs. It engages hard partition whenever it is itself an active
	// ramp-up domain; its per-NUMA cap entries are only the distribution of that
	// aggregate target and never each stand up their own pool.
	if numaID == commonstate.FakedNUMAID {
		return isActiveRampUpDomain(ctx, commonstate.FakedNUMAID)
	}
	c, ok := ctx.RampUpReclaimCPUSetCap[numaID]
	if !ok || c <= 0 {
		return false
	}
	// The global domain distributes its aggregate target across every non-binding
	// backing NUMA, so those NUMAs carry cap entries without themselves hosting a
	// ramp-up source. Such a backing NUMA must not engage the per-NUMA hard
	// partition on its own behalf (the aggregate global entry already publishes the
	// shared pool); only a NUMA that is itself an active ramp-up domain does. This
	// keeps a global ramp-up from fanning out into per-NUMA reclaim entries that
	// would double-count the aggregate pool.
	return isActiveRampUpDomain(ctx, numaID)
}

// isActiveRampUpDomain reports whether numaID is itself one of the ramp-up
// domains active this cycle (FakedNUMAID for the global domain, or a real NUMA
// for a NUMA-binding ramp-up).
func isActiveRampUpDomain(ctx ProvisionContext, numaID int) bool {
	for _, d := range ctx.RampUpDomains {
		if d == numaID {
			return true
		}
	}
	return false
}

// rampUpHardPartitionActiveAnyIn reports whether any of the given NUMAs hosts an
// active ramp-up hard-partition floor. It backs the global (FakedNUMAID) shared
// pool, which aggregates reclaim over nonBindingNUMAs: hard partition must engage
// there when any backing NUMA is an active ramp-up domain, without leaking onto
// dedicated-binding NUMAs that are excluded from nonBindingNUMAs by construction.
func (pa *ProvisionAssemblerCommon) rampUpHardPartitionActiveAnyIn(ctx ProvisionContext, numas machine.CPUSet) bool {
	if ctx.RampUpReclaimCPUSetCap == nil || numas.IsEmpty() {
		return false
	}
	for _, numaID := range numas.ToSliceInt() {
		if c, ok := ctx.RampUpReclaimCPUSetCap[numaID]; ok && c > 0 {
			return true
		}
	}
	return false
}

// effectiveReservedForReclaim sums the pure steady reservation floor across the
// member NUMAs. The reservation floor is NEVER raised by the ramp-up target:
// ramp-up only moves the dynamic ceiling, and a raised floor would (a) pin the
// pool to the ramp-up target and bypass the slow ceiling, and (b) break the
// invariant floor <= pool when initialRatio exceeds MaxRatio. The per-NUMA
// ramp-up target lives in the optional per-scope ceiling instead.
func (pa *ProvisionAssemblerCommon) effectiveReservedForReclaim(ctx ProvisionContext, numas machine.CPUSet) int {
	reserved := 0
	for _, numaID := range numas.ToSliceInt() {
		reserved += ctx.ReservedForReclaim[numaID]
	}
	return reserved
}

// applyDomainScopedLiveReclaimFloor raises reclaimedCoresSize to the QRM-committed
// live size when the current NUMA hosts an active ramp-up domain.
//
// Invariants:
//   - A global (FakedNUMAID) ramp-up MUST NOT broadcast its floor to real NUMAs:
//     global backfill may land on any NUMA, so mid-ramp reclaim on a real NUMA
//     is not guaranteed to stay there and cannot be protected.
//   - The floor is best-effort: skipped when dedicated+live > NUMA capacity,
//     because CPURequest is a hard guarantee for reclaim-disabled dedicated pools.
//   - Returns the (possibly raised) reclaimedCoresSize.
func (pa *ProvisionAssemblerCommon) applyDomainScopedLiveReclaimFloor(
	ctx ProvisionContext,
	numaID int,
	reclaimedCoresSize int,
	shareAndIsolateDedicatedPoolSizes map[string]int,
) int {
	if ctx.LiveReclaimByNUMA == nil {
		return reclaimedCoresSize
	}
	rampUpDomainSet := make(map[int]bool, len(ctx.RampUpDomains))
	for _, d := range ctx.RampUpDomains {
		rampUpDomainSet[d] = true
	}
	domainActive := false
	if numaID == commonstate.FakedNUMAID {
		domainActive = rampUpDomainSet[commonstate.FakedNUMAID]
	} else {
		domainActive = rampUpDomainSet[numaID]
	}
	if !domainActive {
		return reclaimedCoresSize
	}
	liveSize, ok := ctx.LiveReclaimByNUMA[numaID]
	if !ok || liveSize <= reclaimedCoresSize {
		return reclaimedCoresSize
	}
	numaCap := 0
	if pa.metaServer != nil && pa.metaServer.CPUDetails != nil {
		numaCap = pa.metaServer.CPUDetails.CPUsInNUMANodes(numaID).Size()
	}
	totalAllocated := general.SumUpMapValues(shareAndIsolateDedicatedPoolSizes)
	if numaCap <= 0 || totalAllocated+liveSize <= numaCap {
		general.InfoS("apply domain-scoped live reclaim floor",
			"numaID", numaID, "currentReclaim", reclaimedCoresSize,
			"liveReclaim", liveSize, "totalAllocated", totalAllocated,
			"numaCapacity", numaCap)
		return liveSize
	}
	general.InfoS("skip live reclaim floor: capacity overflow",
		"numaID", numaID, "currentReclaim", reclaimedCoresSize,
		"liveReclaim", liveSize, "totalAllocated", totalAllocated,
		"numaCapacity", numaCap)
	return reclaimedCoresSize
}
