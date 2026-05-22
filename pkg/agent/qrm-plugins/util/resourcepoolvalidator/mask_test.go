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

type fakeValidator struct {
	errs map[string]error
}

func (f *fakeValidator) Validate(_ context.Context, poolName string, scope Scope,
	_ v1.ResourceList, _ []v1.ResourceName, _ v1.ResourceList, _ ...string,
) error {
	if f.errs == nil {
		return nil
	}
	key := poolName + ":" + scopeKey(scope)
	return f.errs[key]
}

func scopeKey(s Scope) string {
	if s.IsNodeScope() {
		return "node"
	}
	result := ""
	for i, id := range s.NumaIDs {
		if i > 0 {
			result += ","
		}
		result += string(rune('0' + id))
	}
	return result
}

func (f *fakeValidator) PrefetchNumaAllocations(_ context.Context, _ string, _ []int, _ ...string) (
	map[int]v1.ResourceList, error,
) {
	return nil, nil
}

func TestMaskExceeds_NilOrEmptyInputs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		validator Validator
		poolName  string
		incoming  v1.ResourceList
		resources []v1.ResourceName
		maskBits  []int
	}{
		{name: "nil validator", validator: nil, poolName: "p", incoming: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}, resources: []v1.ResourceName{v1.ResourceCPU}, maskBits: []int{0}},
		{name: "empty pool name", validator: &fakeValidator{}, poolName: "", incoming: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}, resources: []v1.ResourceName{v1.ResourceCPU}, maskBits: []int{0}},
		{name: "empty maskBits", validator: &fakeValidator{}, poolName: "p", incoming: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}, resources: []v1.ResourceName{v1.ResourceCPU}, maskBits: nil},
		{name: "empty resources", validator: &fakeValidator{}, poolName: "p", incoming: v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")}, resources: nil, maskBits: []int{0}},
		{name: "empty incoming", validator: &fakeValidator{}, poolName: "p", incoming: v1.ResourceList{}, resources: []v1.ResourceName{v1.ResourceCPU}, maskBits: []int{0}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := MaskExceeds(context.Background(), tc.validator, tc.poolName, tc.incoming, tc.resources, tc.maskBits, nil, false); got {
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
		false,
	)
	if !got {
		t.Fatalf("expected MaskExceeds=true (3+2 > 4), got false")
	}
}

func TestMaskExceeds_MultiNUMAAggregatedWithinLimit(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
		1: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
	}}
	allocated := &stubAllocatedProvider{allocated: map[string]map[int]v1.ResourceList{
		"p": {
			0: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "3"}),
			1: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1"}),
		},
	}}
	v := NewValidator(pools, allocated)

	// Aggregated: allocated=3+1=4, total incoming=4, max=4+4=8 -> 4+4=8 <= 8 -> ok.
	if MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceCPU: resource.MustParse("4")},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0, 1},
		nil,
		false,
	) {
		t.Fatalf("expected MaskExceeds=false (aggregated 3+1+4=8 <= 8)")
	}

	// Aggregated: allocated=3+1=4, total incoming=5, max=4+4=8 -> 4+5=9 > 8 -> exceeded.
	if !MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceCPU: resource.MustParse("5")},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0, 1},
		nil,
		false,
	) {
		t.Fatalf("expected MaskExceeds=true (aggregated 3+1+5=9 > 8)")
	}
}

func TestMaskExceeds_EvenlyDistributedPerNumaValidation(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
		1: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
	}}
	allocated := &stubAllocatedProvider{allocated: map[string]map[int]v1.ResourceList{
		"p": {
			0: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "3"}),
			1: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1"}),
		},
	}}
	v := NewValidator(pools, allocated)

	// evenlyDistributed=true, mask=[0,1], total=4, per-NUMA=2.
	// NUMA-0: 3+2=5 > 4 -> exceeded.
	if !MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceCPU: resource.MustParse("4")},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0, 1},
		nil,
		true,
	) {
		t.Fatalf("expected MaskExceeds=true (NUMA-0: 3+2=5 > 4)")
	}

	// evenlyDistributed=true, mask=[0,1], total=4, per-NUMA=2.
	// NUMA-1: 1+2=3 <= 4 ok, but NUMA-0 already exceeded above.
	// Now test with lower allocated on NUMA-0.
	allocated2 := &stubAllocatedProvider{allocated: map[string]map[int]v1.ResourceList{
		"p": {
			0: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "2"}),
			1: mustList(map[v1.ResourceName]string{v1.ResourceCPU: "1"}),
		},
	}}
	v2 := NewValidator(pools, allocated2)

	// NUMA-0: 2+2=4 <= 4 ok, NUMA-1: 1+2=3 <= 4 ok -> not exceeded.
	if MaskExceeds(context.Background(), v2, "p",
		v1.ResourceList{v1.ResourceCPU: resource.MustParse("4")},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0, 1},
		nil,
		true,
	) {
		t.Fatalf("expected MaskExceeds=false (NUMA-0: 2+2=4, NUMA-1: 1+2=3)")
	}

	// evenlyDistributed=true with single NUMA mask -> falls through to aggregated
	// path (same as evenlyDistributed=false for single NUMA).
	if !MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceCPU: resource.MustParse("2")},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0},
		nil,
		true,
	) {
		t.Fatalf("expected MaskExceeds=true for single NUMA (3+2 > 4)")
	}
}

func TestMaskExceeds_EvenlyDistributedWithCache(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "8Gi"})}},
		1: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "8Gi"})}},
	}}
	v := NewValidator(pools, &stubAllocatedProvider{})

	// evenlyDistributed=true, total=8Gi, per-NUMA=4Gi.
	// cache: numa-0=5Gi, numa-1=3Gi.
	// NUMA-0: 5Gi+4Gi=9Gi > 8Gi -> exceeded.
	cache := map[int]v1.ResourceList{
		0: {v1.ResourceMemory: *resource.NewQuantity(5<<30, resource.BinarySI)},
		1: {v1.ResourceMemory: *resource.NewQuantity(3<<30, resource.BinarySI)},
	}
	if !MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(8<<30, resource.BinarySI)},
		[]v1.ResourceName{v1.ResourceMemory},
		[]int{0, 1},
		cache,
		true,
	) {
		t.Fatalf("expected true (NUMA-0: 5Gi+4Gi > 8Gi)")
	}

	// cache: numa-0=3Gi, numa-1=3Gi.
	// NUMA-0: 3Gi+4Gi=7Gi <= 8Gi ok, NUMA-1: 3Gi+4Gi=7Gi <= 8Gi ok -> not exceeded.
	cache[0] = v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(3<<30, resource.BinarySI)}
	cache[1] = v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(3<<30, resource.BinarySI)}
	if MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(8<<30, resource.BinarySI)},
		[]v1.ResourceName{v1.ResourceMemory},
		[]int{0, 1},
		cache,
		true,
	) {
		t.Fatalf("expected false (both NUMAs: 3+4=7Gi <= 8Gi)")
	}
}

func TestMaskExceeds_NonCapacityErrorReturnsFalse(t *testing.T) {
	t.Parallel()

	fv := &fakeValidator{errs: map[string]error{
		"p:0": errors.New("transient fetch error"),
	}}
	got := MaskExceeds(context.Background(), fv, "p",
		v1.ResourceList{v1.ResourceCPU: resource.MustParse("1")},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0},
		nil,
		false,
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
		false,
	)
	if !got {
		t.Fatalf("expected true with cache hit (7Gi+2Gi > 8Gi)")
	}
}

func TestMaskExceeds_MultiNUMACacheAggregation(t *testing.T) {
	t.Parallel()

	pools := &stubPoolsProvider{pools: map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "8Gi"})}},
		1: {"p": {PoolName: "p", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "8Gi"})}},
	}}
	v := NewValidator(pools, &stubAllocatedProvider{})

	// cache: numa-0=7Gi, numa-1=6Gi -> aggregated=13Gi, incoming=4Gi, max=16Gi -> 13+4=17 > 16 -> exceeded.
	cache := map[int]v1.ResourceList{
		0: {v1.ResourceMemory: *resource.NewQuantity(7<<30, resource.BinarySI)},
		1: {v1.ResourceMemory: *resource.NewQuantity(6<<30, resource.BinarySI)},
	}
	got := MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(4<<30, resource.BinarySI)},
		[]v1.ResourceName{v1.ResourceMemory},
		[]int{0, 1},
		cache,
		false,
	)
	if !got {
		t.Fatalf("expected true with aggregated cache (7+6+4=17Gi > 16Gi)")
	}

	// cache: numa-0=3Gi, numa-1=2Gi -> aggregated=5Gi, incoming=4Gi, max=16Gi -> 5+4=9 <= 16 -> ok.
	cache[0] = v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(3<<30, resource.BinarySI)}
	cache[1] = v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(2<<30, resource.BinarySI)}
	got = MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceMemory: *resource.NewQuantity(4<<30, resource.BinarySI)},
		[]v1.ResourceName{v1.ResourceMemory},
		[]int{0, 1},
		cache,
		false,
	)
	if got {
		t.Fatalf("expected false with aggregated cache (3+2+4=9Gi <= 16Gi)")
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
		false,
	)
	withCache := MaskExceeds(context.Background(), v, "p",
		v1.ResourceList{v1.ResourceCPU: *resource.NewMilliQuantity(2000, resource.DecimalSI)},
		[]v1.ResourceName{v1.ResourceCPU},
		[]int{0},
		map[int]v1.ResourceList{},
		false,
	)
	if withNil != withCache {
		t.Fatalf("nil cache and empty cache should produce same result: nil=%v, empty=%v", withNil, withCache)
	}
	if !withNil {
		t.Fatalf("expected true (3000m+2000m > 4000m)")
	}
}
