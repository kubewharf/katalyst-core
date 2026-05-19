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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	resourcepool "github.com/kubewharf/katalyst-core/pkg/util/resource-pool"
)

// Scope describes a validation dimension, currently only NUMA-aware. Future
// dimensions (socket, device, ...) can be added as additional fields without
// breaking callers.
type Scope struct {
	// NumaID identifies the NUMA node to validate against.
	// Use resourcepool.NumaIDAll(-1) for node-level validation.
	NumaID int
}

// NodeScope returns a Scope representing the node-level (numa-agnostic) view.
func NodeScope() Scope {
	return Scope{NumaID: resourcepool.NumaIDAll}
}

// NumaScope returns a Scope tied to the given NUMA node id.
func NumaScope(numaID int) Scope {
	return Scope{NumaID: numaID}
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
//     allocations once via PrefetchNumaAllocations, then pass the resulting map
//     entry-by-entry as the `allocated` parameter to Validate inside the mask
//     iteration loop. This avoids O(N×2^(N-1)) redundant GetAllocated calls
//     down to O(N), where N is the number of NUMA nodes.
type Validator interface {
	// Validate returns nil if admitting `incoming` keeps the running total
	// within MaxAllocatable for every resource in `resources`. The caller is
	// responsible for skipping pods without a resource pool annotation.
	//
	// Behavioural rules:
	//   - poolName == "": no-op, returns nil.
	//   - Pool not declared in the requested scope: no constraint, returns nil.
	//   - MaxAllocatable does not declare a particular resource: unconstrained
	//     for this pool (skipped).
	//   - allocated[name] + incoming[name] > MaxAllocatable[name]: returns
	//     CapacityExceededError for the first resource that exceeds.
	//
	// The `allocated` parameter controls how already-allocated quantities are
	// obtained:
	//   - allocated == nil: the method calls GetAllocated internally (on-demand
	//     fetch). Safe for one-off or NodeScope checks.
	//   - allocated != nil: used directly as the pre-fetched allocated
	//     quantity, skipping the GetAllocated call. Intended for the NumaScope
	//     batch path where the caller has already fetched allocations via
	//     PrefetchNumaAllocations. If the map is missing a resource name, its
	//     allocated quantity is treated as zero.
	//
	// excludePodUIDs is forwarded to GetAllocated when allocated == nil so
	// that the specified pods are excluded from the allocated total.
	Validate(ctx context.Context, poolName string, scope Scope,
		incoming v1.ResourceList, resources []v1.ResourceName,
		allocated v1.ResourceList, excludePodUIDs ...string) error

	// PrefetchNumaAllocations batch-fetches per-NUMA allocated quantities for
	// the given pool across all specified NUMA nodes. The returned map (key:
	// numaID) should be passed entry-by-entry as the `allocated` parameter to
	// Validate inside IterateBitMasks loops to avoid redundant GetAllocated
	// calls.
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
// Behavioural rules:
//   - poolName == "": treated as no-op, returns nil.
//   - The pool is not declared in the requested scope: treated as no constraint
//     (returns nil).
//   - MaxAllocatable does not declare a particular resource: that resource is
//     unconstrained for this pool (skipped).
//   - allocated[name] + incoming[name] > MaxAllocatable[name]: returns
//     CapacityExceededError for that resource.
//   - When allocated is nil the method falls back to calling GetAllocated
//     internally.
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

	pool, ok := lookupPool(pools, scope, poolName)
	if !ok || pool.MaxAllocatable == nil {
		return nil
	}

	if allocated == nil {
		allocated, err = v.allocated.GetAllocated(poolName, scope, excludePodUIDs...)
		if err != nil {
			return err
		}
	}

	for _, name := range resources {
		maxAllocatable, hasMax := (*pool.MaxAllocatable)[name]
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
// use the returned map with ValidateWithAllocated to avoid redundant
// GetAllocated invocations when validating multiple NUMA scopes (e.g. inside
// IterateBitMasks).
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

// lookupPool finds a ResourcePool by name within the given scope using O(1)
// map lookup.
func lookupPool(pools map[int]map[string]nodev1alpha1.ResourcePool, scope Scope, poolName string) (nodev1alpha1.ResourcePool, bool) {
	poolMap, ok := pools[scope.NumaID]
	if !ok {
		return nodev1alpha1.ResourcePool{}, false
	}
	p, ok := poolMap[poolName]
	return p, ok
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
	return "resource pool " + e.PoolName + " " + string(e.Resource) +
		" capacity exceeded: allocated=" + e.Allocated.String() +
		", incoming=" + e.Incoming.String() +
		", max=" + e.Max.String()
}

// IsCapacityExceeded reports whether the given error (or any error wrapped
// around it) is a CapacityExceededError.
func IsCapacityExceeded(err error) bool {
	var ce *CapacityExceededError
	return errors.As(err, &ce)
}
