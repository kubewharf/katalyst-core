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
	"testing"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
)

// fakeValidator allows tests to drive arbitrary errors per (poolName, scope)
// without exercising the real validator implementation.
type fakeValidator struct {
	errs map[int]error // numaID -> err returned by Validate/ValidateWithAllocated
}

func (f *fakeValidator) Validate(_ context.Context, _ string, scope Scope,
	_ v1.ResourceList, _ []v1.ResourceName, _ v1.ResourceList, _ ...string,
) error {
	if f.errs == nil {
		return nil
	}
	return f.errs[scope.NumaID]
}

func (f *fakeValidator) PrefetchNumaAllocations(_ context.Context, _ string, _ []int, _ ...string) (
	map[int]v1.ResourceList, error,
) {
	return nil, nil
}

func TestMaskExceeds_NilOrEmptyInputs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		validator       Validator
		poolName        string
		incomingPerNUMA v1.ResourceList
		resources       []v1.ResourceName
		maskBits        []int
	}{
		{name: "nil validator", validator: nil, poolName: "p", incomingPerNUMA: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}, resources: []v1.ResourceName{v1.ResourceCPU}, maskBits: []int{0}},
		{name: "empty pool name", validator: &fakeValidator{}, poolName: "", incomingPerNUMA: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}, resources: []v1.ResourceName{v1.ResourceCPU}, maskBits: []int{0}},
		{name: "empty maskBits", validator: &fakeValidator{}, poolName: "p", incomingPerNUMA: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}, resources: []v1.ResourceName{v1.ResourceCPU}, maskBits: nil},
		{name: "empty resources", validator: &fakeValidator{}, poolName: "p", incomingPerNUMA: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}, resources: nil, maskBits: []int{0}},
		{name: "empty incoming", validator: &fakeValidator{}, poolName: "p", incomingPerNUMA: v1.ResourceList{}, resources: []v1.ResourceName{v1.ResourceCPU}, maskBits: []int{0}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MaskExceeds(context.Background(), tc.validator, tc.poolName, tc.incomingPerNUMA, tc.resources, tc.maskBits, nil); got {
				t.Fatalf("expected false, got true")
			}
		})
	}
}

func TestMaskExceeds_SingleNUMACapacityExceeded(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
	}}
	allocated := &stubAllocatedProvider{allocated: map[string]map[int]v1.ResourceList{
		"p": {0: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "3"})},
	}}
	v := NewValidator(pools, allocated)

	got := MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceCPU: resource.MustParse("2")},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0},
		nil,
	)
	if !got {
		t.Fatalf("expected MaskExceeds=true (3+2 > 4), got false")
	}
}

func TestMaskExceeds_MultiNUMAPerNUMAShareWithinLimit(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
		1: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
	}}
	allocated := &stubAllocatedProvider{allocated: map[string]map[int]v1.ResourceList{
		"p": {
			0: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "3"}),
			1: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "3"}),
		},
	}}
	v := NewValidator(pools, allocated)

	// per-NUMA share = 4/2 = 2; allocated 3 + 2 > 4 -> exceeded.
	if !MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceCPU: resource.MustParse("2")},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0, 1},
		nil,
	) {
		t.Fatalf("expected MaskExceeds=true (per-numa 3+2 > 4)")
	}

	// loosen allocated to 1 each -> per-numa 1+2 <= 4, not exceeded.
	allocated.allocated["p"][0] = mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1"})
	allocated.allocated["p"][1] = mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1"})
	if MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceCPU: resource.MustParse("2")},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0, 1},
		nil,
	) {
		t.Fatalf("expected MaskExceeds=false (per-numa 1+2 <= 4)")
	}
}

func TestMaskExceeds_NonCapacityErrorReturnsFalse(t *testing.T) {
	t.Parallel()

	fv := &fakeValidator{errs: map[int]error{0: errors.New("transient fetch error")}}
	got := MaskExceeds(context.Background(), fv, "p",
		v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0},
		nil,
	)
	if got {
		t.Fatalf("expected MaskExceeds=false on non-capacity error, got true")
	}
}

func TestMaskExceeds_CacheHit(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "8Gi"})}},
	}}
	v := NewValidator(pools, &stubAllocatedProvider{})

	cache := map[int]v1.ResourceList{
		0: {v1.ResourceMemory: *resource.NewQuantity(7<<30, resource.BinarySI)},
	}
	got := MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(2<<30, resource.BinarySI)},
		[]v1.ResourceName{v1.ResourceMemory},
		[]int{0},
		cache,
	)
	if !got {
		t.Fatalf("expected true with cache hit (7Gi+2Gi > 8Gi)")
	}
}

func TestMaskExceeds_PartialCacheMiss(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "8Gi"})}},
		1: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "8Gi"})}},
	}}
	allocated := &stubAllocatedProvider{allocated: map[string]map[int]v1.ResourceList{
		"p": {1: {v1.ResourceMemory: *resource.NewQuantity(6<<30, resource.BinarySI)}},
	}}
	v := NewValidator(pools, allocated)

	// cache has numa-0 but not numa-1; numa-1 falls back to GetAllocated.
	cache := map[int]v1.ResourceList{
		0: {v1.ResourceMemory: *resource.NewQuantity(7<<30, resource.BinarySI)},
	}
	got := MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(2<<30, resource.BinarySI)},
		[]v1.ResourceName{v1.ResourceMemory},
		[]int{0, 1},
		cache,
	)
	if !got {
		t.Fatalf("expected true: numa-0 from cache (7+2>8), numa-1 fallback (6+2=8<=8 but numa-0 already exceeds)")
	}
}

func TestMaskExceeds_NilCacheEquivalentToNoCache(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
	}}
	allocated := &stubAllocatedProvider{allocated: map[string]map[int]v1.ResourceList{
		"p": {0: {v1.ResourceCPU: *resource.NewMilliQuantity(3000, resource.DecimalSI)}},
	}}
	v := NewValidator(pools, allocated)

	withNil := MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceCPU: *resource.NewMilliQuantity(2000, resource.DecimalSI)},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0},
		nil,
	)
	withCache := MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceCPU: *resource.NewMilliQuantity(2000, resource.DecimalSI)},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0},
		map[int]v1.ResourceList{}, // empty non-nil cache -> still miss -> fallback
	)
	if withNil != withCache {
		t.Fatalf("nil cache and empty cache should produce same result: nil=%v, empty=%v", withNil, withCache)
	}
	if !withNil {
		t.Fatalf("expected true (3000m+2000m > 4000m)")
	}
}
