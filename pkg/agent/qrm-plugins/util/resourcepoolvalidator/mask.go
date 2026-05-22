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

package resourcepoolvalidator

import (
	"context"

	v1 "k8s.io/api/core/v1"

	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// MaskExceeds reports whether admitting `incoming` (total request, not
// per-NUMA) to the NUMA nodes referenced by `maskBits` would exceed the
// pool's capacity for any resource in `resources`.
//
// Validation mode depends on `evenlyDistributed`:
//
//   - evenlyDistributed=false: aggregated validation. Both allocated and
//     MaxAllocatable are summed across all NUMAs in the mask, then checked:
//
//     sum(allocated[numaID]) + incoming <= sum(MaxAllocatable[numaID])
//
//     This allows non-uniform NUMA distribution.
//
//   - evenlyDistributed=true (and len(maskBits) > 1): per-NUMA validation.
//     The incoming request is divided evenly across NUMAs and each NUMA is
//     validated independently:
//
//     allocated[numaID] + (incoming / len(maskBits)) <= MaxAllocatable[numaID]
//
//     This is required when the workload must be evenly distributed across
//     NUMAs (distributeEvenlyAcrossNuma annotation).
//
// When allocCache is non-nil it is used as a pre-fetched allocation map (key:
// numaID) to avoid redundant Validator.GetAllocated calls across multiple
// masks. Callers that iterate many masks over the same set of NUMA nodes
// (e.g. inside IterateBitMasks) should pre-populate the cache via
// Validator.PrefetchNumaAllocations once before the loop. When allocCache is
// nil or does not contain entries for some NUMAs in the mask, those entries
// are treated as zero and the full aggregated allocated is fetched on demand
// via GetAllocated.
//
// excludePodUIDs is forwarded to Validator.Validate (and ultimately
// GetAllocated) so that the specified pods are excluded from the allocated
// total. This avoids double-counting during inplace-update-resize.
func MaskExceeds(
	ctx context.Context,
	validator Validator,
	poolName string,
	incoming v1.ResourceList,
	resources []v1.ResourceName,
	maskBits []int,
	allocCache map[int]v1.ResourceList,
	evenlyDistributed bool,
	excludePodUIDs ...string,
) bool {
	if validator == nil || poolName == "" {
		return false
	}
	if len(maskBits) == 0 || len(resources) == 0 || len(incoming) == 0 {
		return false
	}

	if evenlyDistributed && len(maskBits) > 1 {
		return maskExceedsPerNuma(ctx, validator, poolName, incoming, resources, maskBits, allocCache, excludePodUIDs...)
	}
	return maskExceedsAggregated(ctx, validator, poolName, incoming, resources, maskBits, allocCache, excludePodUIDs...)
}

// maskExceedsAggregated performs the aggregated (cross-NUMA) validation:
// sum(allocated) + incoming <= sum(MaxAllocatable).
func maskExceedsAggregated(
	ctx context.Context,
	validator Validator,
	poolName string,
	incoming v1.ResourceList,
	resources []v1.ResourceName,
	maskBits []int,
	allocCache map[int]v1.ResourceList,
	excludePodUIDs ...string,
) bool {
	var aggregatedAlloc v1.ResourceList
	if allocCache != nil {
		aggregatedAlloc = aggregateAllocCache(allocCache, maskBits, resources)
	}

	err := validator.Validate(ctx, poolName, NumaScope(maskBits...),
		incoming, resources, aggregatedAlloc, excludePodUIDs...)
	if err == nil {
		return false
	}
	if IsCapacityExceeded(err) {
		general.Infof("ResourcePool %q exceeds NumaScope=%v (mask=%v): %v",
			poolName, maskBits, maskBits, err)
		return true
	}
	general.Warningf("ResourcePool MaskExceeds pool=%q mask=%v ignored err: %v",
		poolName, maskBits, err)
	return false
}

// maskExceedsPerNuma performs per-NUMA validation: the incoming request is
// divided evenly by len(maskBits) and each NUMA is validated independently.
// Any single NUMA exceeding capacity causes the mask to be rejected.
func maskExceedsPerNuma(
	ctx context.Context,
	validator Validator,
	poolName string,
	incoming v1.ResourceList,
	resources []v1.ResourceName,
	maskBits []int,
	allocCache map[int]v1.ResourceList,
	excludePodUIDs ...string,
) bool {
	numaCount := int64(len(maskBits))
	incomingPerNUMA := make(v1.ResourceList, len(incoming))
	for name, q := range incoming {
		perNumaQ := q.DeepCopy()
		perNumaQ.Set(q.Value() / numaCount)
		incomingPerNUMA[name] = perNumaQ
	}

	for _, numaID := range maskBits {
		var perNumaAlloc v1.ResourceList
		if allocCache != nil {
			if entry, ok := allocCache[numaID]; ok {
				perNumaAlloc = entry
			}
		}

		err := validator.Validate(ctx, poolName, NumaScope(numaID),
			incomingPerNUMA, resources, perNumaAlloc, excludePodUIDs...)
		if err == nil {
			continue
		}
		if IsCapacityExceeded(err) {
			general.Infof("ResourcePool %q exceeds NumaScope=%d (mask=%v, evenly distributed): %v",
				poolName, numaID, maskBits, err)
			return true
		}
		general.Warningf("ResourcePool MaskExceeds pool=%q numa=%d mask=%v ignored err: %v",
			poolName, numaID, maskBits, err)
		return false
	}
	return false
}

// aggregateAllocCache sums the per-NUMA allocated quantities from allocCache
// for all NUMA IDs in maskBits. Only resources listed in `resources` are
// included. If a NUMA ID is missing from the cache, its contribution is
// treated as zero. Returns nil if no cache entries are found, which signals
// Validate to fetch on demand.
func aggregateAllocCache(allocCache map[int]v1.ResourceList, maskBits []int, resources []v1.ResourceName) v1.ResourceList {
	result := v1.ResourceList{}
	found := false
	for _, nid := range maskBits {
		perNuma, ok := allocCache[nid]
		if !ok {
			continue
		}
		found = true
		for _, name := range resources {
			if q, has := perNuma[name]; has {
				if existing, has := result[name]; has {
					existing.Add(q)
					result[name] = existing
				} else {
					result[name] = q.DeepCopy()
				}
			}
		}
	}
	if !found {
		return nil
	}
	return result
}
