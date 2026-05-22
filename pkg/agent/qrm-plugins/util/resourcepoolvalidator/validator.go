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

// Package resourcepoolvalidator implements a generic resource-pool capacity
// validator that QRM plugins can use to enforce the per-pool MaxAllocatable
// constraint across one or more resource dimensions (cpu, memory, ...).
//
// Pods that do not declare a resource pool annotation are expected to be
// filtered out by the caller via GetResourcePoolName before invoking the
// validator.
package resourcepoolvalidator

import (
	"context"
	"errors"
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	resourcepool "github.com/kubewharf/katalyst-core/pkg/util/resource-pool"
)

// Scope describes a validation dimension, currently only NUMA-aware. Future
// dimensions (socket, device, ...) can be added as additional fields without
// breaking callers.
type Scope struct {
	// NumaIDs identifies the NUMA nodes to validate against.
	// An empty slice represents node-level validation (NodeScope).
	// A non-empty slice represents one or more NUMA nodes (NumaScope).
	NumaIDs []int
}

// IsNodeScope reports whether the scope represents a node-level (numa-agnostic)
// view, i.e. NumaIDs is empty.
func (s Scope) IsNodeScope() bool {
	return len(s.NumaIDs) == 0
}

// NodeScope returns a Scope representing the node-level (numa-agnostic) view.
func NodeScope() Scope {
	return Scope{}
}

// NumaScope returns a Scope tied to the given NUMA node ids.
// A single id produces a single-NUMA scope; multiple ids produce a
// multi-NUMA scope whose allocated and MaxAllocatable are aggregated across
// all listed NUMAs.
func NumaScope(numaIDs ...int) Scope {
	return Scope{NumaIDs: numaIDs}
}

// ResourcePoolsProvider returns the resource pool division for the current
// node. The returned map keys are NUMA ids (resourcepool.NumaIDAll(-1)
// represents node-level pools), and the inner map is keyed by pool name for
// O(1) lookup.
//
// MetaServer's ResourcePoolManager satisfies this interface natively.
type ResourcePoolsProvider interface {
	NodeResourcePools(ctx context.Context) (map[int]map[string]nodev1alpha1.ResourcePool, error)
}

// AllocatedProvider returns the already-allocated resource amounts for a given
// (poolName, scope) pair. Each QRM plugin implements this against its own
// in-memory state so the validator stays agnostic of plugin internals.
//
// When scope.NumaIDs contains multiple NUMA IDs, the implementation must
// aggregate the allocated quantities across all listed NUMAs.
//
// excludePodUIDs lists pod UIDs that should be excluded from the allocated
// total. This is needed during inplace-update-resize: the current pod's
// existing allocation is already counted in the state, and the incoming
// request represents the full new request, so excluding the pod avoids
// double-counting.
type AllocatedProvider interface {
	GetAllocated(poolName string, scope Scope, excludePodUIDs ...string) (v1.ResourceList, error)
}

// Validator checks that admitting an incoming request to the given pool would
// not exceed the pool's MaxAllocatable across the requested resources.
//
// Two complementary optimisation paths are exposed:
//
//   - NodeScope entry guard: call Validate directly with allocated=nil; the
//     method fetches allocated quantities on demand via the AllocatedProvider.
//     This is the simplest path and is suitable for one-off checks (e.g. the
//     validateResourcePool NodeScope guard at GetTopologyHints entry).
//
//   - NumaScope batch guard (IterateBitMasks): pre-fetch all per-NUMA
//     allocations once via PrefetchNumaAllocations, then aggregate the cache
//     entries for the mask and pass the result as the `allocated` parameter to
//     Validate. This avoids O(N×2^(N-1)) redundant GetAllocated calls down to
//     O(N), where N is the number of NUMA nodes.
type Validator interface {
	// Validate returns nil if admitting `incoming` keeps the running total
	// within MaxAllocatable for every resource in `resources`. The caller is
	// responsible for skipping pods without a resource pool annotation.
	//
	// Behavioral rules:
	//   - poolName == "": no-op, returns nil.
	//   - Pool not declared in the requested scope: no constraint, returns nil.
	//   - MaxAllocatable does not declare a particular resource: unconstrained
	//     for this pool (skipped).
	//   - allocated[name] + incoming[name] > MaxAllocatable[name]: returns
	//     CapacityExceededError for the first resource that exceeds.
	//
	// For NodeScope (empty NumaIDs), MaxAllocatable is looked up from the
	// node-level pool entry. For NumaScope (non-empty NumaIDs), MaxAllocatable
	// and allocated are aggregated across all NUMA IDs in the scope.
	//
	// The `allocated` parameter controls how already-allocated quantities are
	// obtained:
	//   - allocated == nil: the method calls GetAllocated internally (on-demand
	//     fetch). Safe for one-off or NodeScope checks.
	//   - allocated != nil: used directly as the pre-fetched allocated
	//     quantity, skipping the GetAllocated call. Intended for the NumaScope
	//     batch path where the caller has already fetched allocations via
	//     PrefetchNumaAllocations and aggregated them for the mask. If the map
	//     is missing a resource name, its allocated quantity is treated as zero.
	//
	// excludePodUIDs is forwarded to GetAllocated when allocated == nil so
	// that the specified pods are excluded from the allocated total.
	Validate(ctx context.Context, poolName string, scope Scope,
		incoming v1.ResourceList, resources []v1.ResourceName,
		allocated v1.ResourceList, excludePodUIDs ...string) error

	// PrefetchNumaAllocations batch-fetches per-NUMA allocated quantities for
	// the given pool across all specified NUMA nodes. The returned map (key:
	// numaID) should be aggregated by the caller for the mask's NUMA set and
	// passed as the `allocated` parameter to Validate inside IterateBitMasks
	// loops to avoid redundant GetAllocated calls.
	//
	// excludePodUIDs is forwarded to GetAllocated so that the specified pods
	// are excluded from the allocated total.
	//
	// Returns nil if the provider is nil, poolName is empty, or numaIDs is
	// empty. Returns an error if any individual GetAllocated call fails.
	PrefetchNumaAllocations(ctx context.Context, poolName string, numaIDs []int, excludePodUIDs ...string) (
		map[int]v1.ResourceList, error)
}

// NewValidator builds a Validator backed by the given providers.
func NewValidator(pools ResourcePoolsProvider, allocated AllocatedProvider) Validator {
	return &validator{
		pools:     pools,
		allocated: allocated,
	}
}

type validator struct {
	pools     ResourcePoolsProvider
	allocated AllocatedProvider
}

// Validate implements Validator.
//
// Behavioral rules:
//   - poolName == "": treated as no-op, returns nil.
//   - The pool is not declared in the requested scope: treated as no constraint
//     (returns nil).
//   - MaxAllocatable does not declare a particular resource: that resource is
//     unconstrained for this pool (skipped).
//   - allocated[name] + incoming[name] > MaxAllocatable[name]: returns
//     CapacityExceededError for that resource.
//   - When allocated is nil the method falls back to calling GetAllocated
//     internally.
//
// For NodeScope, the node-level pool entry is used directly. For NumaScope,
// MaxAllocatable is aggregated across all NUMA IDs in the scope.
func (v *validator) Validate(ctx context.Context, poolName string, scope Scope,
	incoming v1.ResourceList, resources []v1.ResourceName,
	allocated v1.ResourceList, excludePodUIDs ...string,
) error {
	if poolName == "" {
		return nil
	}

	pools, err := v.pools.NodeResourcePools(ctx)
	if err != nil {
		return err
	}

	var maxAlloc v1.ResourceList
	if scope.IsNodeScope() {
		pool, ok := lookupPool(pools, resourcepool.NumaIDAll, poolName)
		if !ok || pool.MaxAllocatable == nil {
			return nil
		}
		maxAlloc = *pool.MaxAllocatable
	} else {
		found := lookupPools(pools, scope.NumaIDs, poolName)
		if len(found) == 0 {
			return nil
		}
		maxAlloc = aggregateMaxAllocatable(found, resources)
		if len(maxAlloc) == 0 {
			return nil
		}
	}

	if allocated == nil {
		allocated, err = v.allocated.GetAllocated(poolName, scope, excludePodUIDs...)
		if err != nil {
			return err
		}
	}

	for _, name := range resources {
		maxAllocatable, hasMax := maxAlloc[name]
		if !hasMax {
			continue
		}
		incomingQ := incoming[name]
		if incomingQ.IsZero() {
			continue
		}

		used := allocated[name]
		total := used.DeepCopy()
		total.Add(incomingQ)

		if total.Cmp(maxAllocatable) > 0 {
			return &CapacityExceededError{
				PoolName:  poolName,
				Scope:     scope,
				Resource:  name,
				Allocated: used.DeepCopy(),
				Incoming:  incomingQ.DeepCopy(),
				Max:       maxAllocatable.DeepCopy(),
			}
		}
	}
	return nil
}

// PrefetchNumaAllocations batch-fetches per-NUMA allocated quantities for the
// given pool across all specified NUMA nodes in a single pass. Callers should
// aggregate the returned per-NUMA entries for their mask's NUMA set and pass
// the result to Validate to avoid redundant GetAllocated invocations when
// validating multiple masks (e.g. inside IterateBitMasks).
//
// Returns nil if the provider or pool name is empty, or numaIDs is empty.
func (v *validator) PrefetchNumaAllocations(
	ctx context.Context, poolName string, numaIDs []int, excludePodUIDs ...string,
) (map[int]v1.ResourceList, error) {
	if v == nil || v.allocated == nil || poolName == "" || len(numaIDs) == 0 {
		return nil, nil
	}
	result := make(map[int]v1.ResourceList, len(numaIDs))
	for _, nid := range numaIDs {
		alloc, err := v.allocated.GetAllocated(poolName, NumaScope(nid), excludePodUIDs...)
		if err != nil {
			return nil, err
		}
		result[nid] = alloc
	}
	return result, nil
}

// lookupPool finds a ResourcePool by name within a single scope key using O(1)
// map lookup.
func lookupPool(pools map[int]map[string]nodev1alpha1.ResourcePool, scopeKey int, poolName string) (nodev1alpha1.ResourcePool, bool) {
	poolMap, ok := pools[scopeKey]
	if !ok {
		return nodev1alpha1.ResourcePool{}, false
	}
	p, ok := poolMap[poolName]
	return p, ok
}

// lookupPools finds ResourcePools by name across multiple NUMA IDs. Returns a
// map keyed by numaID containing only the NUMAs where the pool was found.
func lookupPools(pools map[int]map[string]nodev1alpha1.ResourcePool, numaIDs []int, poolName string) map[int]nodev1alpha1.ResourcePool {
	result := make(map[int]nodev1alpha1.ResourcePool, len(numaIDs))
	for _, nid := range numaIDs {
		if p, ok := lookupPool(pools, nid, poolName); ok {
			result[nid] = p
		}
	}
	return result
}

// aggregateMaxAllocatable sums the MaxAllocatable quantities across all given
// pools for the requested resources. Only resources present in at least one
// pool's MaxAllocatable are included in the result.
func aggregateMaxAllocatable(pools map[int]nodev1alpha1.ResourcePool, resources []v1.ResourceName) v1.ResourceList {
	result := v1.ResourceList{}
	for _, p := range pools {
		if p.MaxAllocatable == nil {
			continue
		}
		for _, name := range resources {
			if q, ok := (*p.MaxAllocatable)[name]; ok {
				if existing, has := result[name]; has {
					existing.Add(q)
					result[name] = existing
				} else {
					result[name] = q.DeepCopy()
				}
			}
		}
	}
	return result
}

// CapacityExceededError indicates that admitting a request would exceed the
// pool's MaxAllocatable for a particular resource.
type CapacityExceededError struct {
	PoolName  string
	Scope     Scope
	Resource  v1.ResourceName
	Allocated resource.Quantity
	Incoming  resource.Quantity
	Max       resource.Quantity
}

// Error implements the error interface.
func (e *CapacityExceededError) Error() string {
	scopeStr := "NodeScope"
	if !e.Scope.IsNodeScope() {
		scopeStr = fmt.Sprintf("NumaScope=%v", e.Scope.NumaIDs)
	}
	return "resource pool " + e.PoolName + " " + string(e.Resource) +
		" capacity exceeded: allocated=" + e.Allocated.String() +
		", incoming=" + e.Incoming.String() +
		", max=" + e.Max.String() +
		", scope=" + scopeStr
}

// IsCapacityExceeded reports whether the given error (or any error wrapped
// around it) is a CapacityExceededError.
func IsCapacityExceeded(err error) bool {
	var ce *CapacityExceededError
	return errors.As(err, &ce)
}
