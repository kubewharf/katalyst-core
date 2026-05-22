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
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	resourcepool "github.com/kubewharf/katalyst-core/pkg/util/resource-pool"
)

func TestCachedResourcePoolsProvider_InitialSync(t *testing.T) {
	t.Parallel()

	initial := map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}
	stub := &stubPoolsProvider{pools: initial}
	cached := NewCachedResourcePoolsProvider(stub)

	got, err := cached.NodeResourcePools(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 numa entry, got %d", len(got))
	}
	poolMap, ok := got[resourcepool.NumaIDAll]
	if !ok {
		t.Fatal("expected NumaIDAll entry")
	}
	p, ok := poolMap["p1"]
	if !ok {
		t.Fatal("expected pool p1")
	}
	if p.PoolName != "p1" {
		t.Fatalf("expected pool name p1, got %s", p.PoolName)
	}
	if stub.calls != 1 {
		t.Fatalf("expected 1 call during construction, got %d", stub.calls)
	}
}

func TestCachedResourcePoolsProvider_AsyncRefresh(t *testing.T) {
	t.Parallel()

	initial := map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}
	stub := &stubPoolsProvider{pools: initial}
	cached := NewCachedResourcePoolsProvider(stub)
	cached.interval = 100 * time.Millisecond

	stopCh := make(chan struct{})
	go cached.Run(stopCh)

	stub.mu.Lock()
	stub.pools = map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "20"})}},
	}
	stub.mu.Unlock()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := cached.NodeResourcePools(context.Background())
		if poolMap, ok := got[resourcepool.NumaIDAll]; ok {
			if p, ok := poolMap["p1"]; ok && p.MaxAllocatable != nil {
				q := (*p.MaxAllocatable)[v1.ResourceCPU]
				if q.Cmp(resource.MustParse("20")) == 0 {
					close(stopCh)
					return
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	close(stopCh)
	t.Fatal("timed out waiting for cache refresh")
}

func TestCachedResourcePoolsProvider_FailedRefreshKeepsOld(t *testing.T) {
	t.Parallel()

	initial := map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}
	stub := &stubPoolsProvider{pools: initial}
	cached := NewCachedResourcePoolsProvider(stub)
	cached.interval = 100 * time.Millisecond

	stopCh := make(chan struct{})
	go cached.Run(stopCh)

	stub.mu.Lock()
	stub.err = errors.New("transient failure")
	stub.mu.Unlock()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		got, _ := cached.NodeResourcePools(context.Background())
		if poolMap, ok := got[resourcepool.NumaIDAll]; ok {
			if p, ok := poolMap["p1"]; ok && p.MaxAllocatable != nil {
				q := (*p.MaxAllocatable)[v1.ResourceCPU]
				if q.Cmp(resource.MustParse("10")) == 0 {
					stub.mu.Lock()
					calls := stub.calls
					stub.mu.Unlock()
					if calls >= 2 {
						close(stopCh)
						return
					}
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	close(stopCh)
	t.Fatal("cache was cleared after failed refresh")
}
