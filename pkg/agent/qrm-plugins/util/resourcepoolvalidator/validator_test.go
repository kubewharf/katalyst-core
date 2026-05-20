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
	"errors"
	"sync"
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	resourcepool "github.com/kubewharf/katalyst-core/pkg/util/resource-pool"
)

type stubPoolsProvider struct {
	mu    sync.Mutex
	pools map[int]map[string]nodev1alpha1.ResourcePool
	err   error
	calls int
}

func (s *stubPoolsProvider) NodeResourcePools(_ context.Context) (map[int]map[string]nodev1alpha1.ResourcePool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	return s.pools, s.err
}

type stubAllocatedProvider struct {
	allocated map[string]map[int]v1.ResourceList
	err       error
	calls     int
}

func (s *stubAllocatedProvider) GetAllocated(poolName string, scope Scope, _ ...string) (v1.ResourceList, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if byPool, ok := s.allocated[poolName]; ok {
		if scope.IsNodeScope() {
			if rl, ok := byPool[resourcepool.NumaIDAll]; ok {
				return rl.DeepCopy(), nil
			}
			return nil, nil
		}
		result := v1.ResourceList{}
		for _, nid := range scope.NumaIDs {
			if rl, ok := byPool[nid]; ok {
				for name, q := range rl {
					if existing, has := result[name]; has {
						existing.Add(q)
						result[name] = existing
					} else {
						result[name] = q.DeepCopy()
					}
				}
			}
		}
		if len(result) > 0 {
			return result, nil
		}
	}
	return nil, nil
}

func mustList(items map[v1.ResourceName]string) v1.ResourceList {
	out := v1.ResourceList{}
	for k, v := range items {
		out[k] = resource.MustParse(v)
	}
	return out
}

func mustListPtr(items map[v1.ResourceName]string) *v1.ResourceList {
	rl := mustList(items)
	return &rl
}

func TestValidator_EmptyPoolName(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}}
	allocated := &stubAllocatedProvider{}
	v := NewValidator(pools, allocated)

	if err := v.Validate(context.Background(), "", NodeScope(), nil, nil, nil); err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if pools.calls != 0 || allocated.calls != 0 {
		t.Fatalf("expected providers untouched when poolName is empty, got pools.calls=%d allocated.calls=%d", pools.calls, allocated.calls)
	}
}

func TestValidator_PoolNotFound(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"other": {PoolName: "other", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}}
	allocated := &stubAllocatedProvider{}
	v := NewValidator(pools, allocated)

	err := v.Validate(context.Background(), "missing", NodeScope(),
		mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1"}),
		[]v1.ResourceName{v1.ResourceCPU}, nil)
	if err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
	if allocated.calls != 0 {
		t.Fatalf("expected allocated provider untouched when pool not found, got %d", allocated.calls)
	}
}

func TestValidator_NilOrMissingMaxAllocatable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		max  *v1.ResourceList
	}{
		{name: "nil-max", max: nil},
		{name: "max-missing-resource", max: mustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "1Gi"})},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
				resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: tc.max}},
			}}
			allocated := &stubAllocatedProvider{}
			v := NewValidator(pools, allocated)

			err := v.Validate(context.Background(), "p1", NodeScope(),
				mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1000"}),
				[]v1.ResourceName{v1.ResourceCPU}, nil)
			if err != nil {
				t.Fatalf("expected nil err, got %v", err)
			}
		})
	}
}

func TestValidator_WithinCapacity(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}}
	allocated := &stubAllocatedProvider{
		allocated: map[string]map[int]v1.ResourceList{
			"p1": {resourcepool.NumaIDAll: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "4"})},
		},
	}
	v := NewValidator(pools, allocated)

	err := v.Validate(context.Background(), "p1", NodeScope(),
		mustList(map[v1.ResourceName]string{v1.ResourceCPU: "6"}),
		[]v1.ResourceName{v1.ResourceCPU}, nil)
	if err != nil {
		t.Fatalf("expected nil err when allocated+incoming==max, got %v", err)
	}
}

func TestValidator_CapacityExceeded(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}}
	allocated := &stubAllocatedProvider{
		allocated: map[string]map[int]v1.ResourceList{
			"p1": {resourcepool.NumaIDAll: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "8"})},
		},
	}
	v := NewValidator(pools, allocated)

	err := v.Validate(context.Background(), "p1", NodeScope(),
		mustList(map[v1.ResourceName]string{v1.ResourceCPU: "3"}),
		[]v1.ResourceName{v1.ResourceCPU}, nil)
	if !IsCapacityExceeded(err) {
		t.Fatalf("expected CapacityExceededError, got %v", err)
	}
	ce := err.(*CapacityExceededError)
	if ce.PoolName != "p1" || ce.Resource != v1.ResourceCPU || !ce.Scope.IsNodeScope() {
		t.Fatalf("unexpected error fields: %+v", ce)
	}
	if ce.Max.Cmp(resource.MustParse("10")) != 0 {
		t.Fatalf("unexpected max: %s", ce.Max.String())
	}
	if ce.Allocated.Cmp(resource.MustParse("8")) != 0 {
		t.Fatalf("unexpected allocated: %s", ce.Allocated.String())
	}
	if ce.Incoming.Cmp(resource.MustParse("3")) != 0 {
		t.Fatalf("unexpected incoming: %s", ce.Incoming.String())
	}
	if got := ce.Error(); got == "" {
		t.Fatalf("expected non-empty error message")
	}
}

func TestValidator_PerNumaScope(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "100"})}},
		0:                      {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}}
	allocated := &stubAllocatedProvider{
		allocated: map[string]map[int]v1.ResourceList{
			"p1": {
				resourcepool.NumaIDAll: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "20"}),
				0:                      mustList(map[v1.ResourceName]string{v1.ResourceCPU: "8"}),
			},
		},
	}
	v := NewValidator(pools, allocated)

	// Node scope: 20+5 < 100 -> ok
	if err := v.Validate(context.Background(), "p1", NodeScope(),
		mustList(map[v1.ResourceName]string{v1.ResourceCPU: "5"}),
		[]v1.ResourceName{v1.ResourceCPU}, nil); err != nil {
		t.Fatalf("expected nil err for node scope, got %v", err)
	}

	// NUMA-0 scope: 8+5 > 10 -> error
	err := v.Validate(context.Background(), "p1", NumaScope(0),
		mustList(map[v1.ResourceName]string{v1.ResourceCPU: "5"}),
		[]v1.ResourceName{v1.ResourceCPU}, nil)
	if !IsCapacityExceeded(err) {
		t.Fatalf("expected CapacityExceededError for numa-0 scope, got %v", err)
	}
	ce := err.(*CapacityExceededError)
	if len(ce.Scope.NumaIDs) != 1 || ce.Scope.NumaIDs[0] != 0 {
		t.Fatalf("expected scope numa=[0], got %v", ce.Scope.NumaIDs)
	}
}

func TestValidator_MultiNumaScopeAggregated(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
		1: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
	}}
	allocated := &stubAllocatedProvider{
		allocated: map[string]map[int]v1.ResourceList{
			"p1": {
				0: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "3"}),
				1: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1"}),
			},
		},
	}
	v := NewValidator(pools, allocated)

	// Aggregated: allocated=3+1=4, incoming=4, max=4+4=8 -> 4+4=8 <= 8 -> ok
	err := v.Validate(context.Background(), "p1", NumaScope(0, 1),
		mustList(map[v1.ResourceName]string{v1.ResourceCPU: "4"}),
		[]v1.ResourceName{v1.ResourceCPU}, nil)
	if err != nil {
		t.Fatalf("expected nil err for aggregated multi-numa scope, got %v", err)
	}

	// Aggregated: allocated=3+1=4, incoming=5, max=4+4=8 -> 4+5=9 > 8 -> exceeded
	err = v.Validate(context.Background(), "p1", NumaScope(0, 1),
		mustList(map[v1.ResourceName]string{v1.ResourceCPU: "5"}),
		[]v1.ResourceName{v1.ResourceCPU}, nil)
	if !IsCapacityExceeded(err) {
		t.Fatalf("expected CapacityExceededError for aggregated overflow, got %v", err)
	}
}

func TestValidator_MultiResource(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{
			v1.ResourceCPU:    "10",
			v1.ResourceMemory: "10Gi",
		})}},
	}}
	allocated := &stubAllocatedProvider{
		allocated: map[string]map[int]v1.ResourceList{
			"p1": {resourcepool.NumaIDAll: mustList(map[v1.ResourceName]string{
				v1.ResourceCPU:    "1",
				v1.ResourceMemory: "9Gi",
			})},
		},
	}
	v := NewValidator(pools, allocated)

	// CPU 1+1=2<=10, memory 9Gi+2Gi=11Gi>10Gi -> error reports memory.
	err := v.Validate(context.Background(), "p1", NodeScope(),
		mustList(map[v1.ResourceName]string{
			v1.ResourceCPU:    "1",
			v1.ResourceMemory: "2Gi",
		}),
		[]v1.ResourceName{v1.ResourceCPU, v1.ResourceMemory}, nil)
	if !IsCapacityExceeded(err) {
		t.Fatalf("expected CapacityExceededError, got %v", err)
	}
	ce := err.(*CapacityExceededError)
	if ce.Resource != v1.ResourceMemory {
		t.Fatalf("expected resource=memory, got %s", ce.Resource)
	}
}

func TestValidator_PoolsProviderError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	pools := &stubPoolsProvider{err: wantErr}
	allocated := &stubAllocatedProvider{}
	v := NewValidator(pools, allocated)

	err := v.Validate(context.Background(), "p1", NodeScope(),
		mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1"}),
		[]v1.ResourceName{v1.ResourceCPU}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected pool provider error to propagate, got %v", err)
	}
	if allocated.calls != 0 {
		t.Fatalf("allocated provider should not be called on pools error")
	}
}

func TestValidator_AllocatedProviderError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}}
	allocated := &stubAllocatedProvider{err: wantErr}
	v := NewValidator(pools, allocated)

	err := v.Validate(context.Background(), "p1", NodeScope(),
		mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1"}),
		[]v1.ResourceName{v1.ResourceCPU}, nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected allocated provider error to propagate, got %v", err)
	}
}

func TestValidator_ZeroIncoming(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "1"})}},
	}}
	allocated := &stubAllocatedProvider{
		allocated: map[string]map[int]v1.ResourceList{
			"p1": {resourcepool.NumaIDAll: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1"})},
		},
	}
	v := NewValidator(pools, allocated)

	err := v.Validate(context.Background(), "p1", NodeScope(),
		v1.ResourceList{v1.ResourceCPU: resource.Quantity{}},
		[]v1.ResourceName{v1.ResourceCPU}, nil)
	if err != nil {
		t.Fatalf("expected nil err for zero incoming, got %v", err)
	}
}

func TestScope_IsNodeScope(t *testing.T) {
	t.Parallel()

	if !NodeScope().IsNodeScope() {
		t.Fatalf("expected NodeScope().IsNodeScope() == true")
	}
	if NumaScope(0).IsNodeScope() {
		t.Fatalf("expected NumaScope(0).IsNodeScope() == false")
	}
	if NumaScope(0, 1).IsNodeScope() {
		t.Fatalf("expected NumaScope(0,1).IsNodeScope() == false")
	}
}
