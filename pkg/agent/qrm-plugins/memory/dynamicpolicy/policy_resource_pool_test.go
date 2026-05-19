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

package dynamicpolicy

import (
	"context"
	"testing"

	info "github.com/google/cadvisor/info/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/memory/dynamicpolicy/state"
	rpvalidator "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/resourcepoolvalidator"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepool "github.com/kubewharf/katalyst-core/pkg/util/resource-pool"
)

// stubMemoryReadonlyState implements state.ReadonlyState with only
// GetPodResourceEntries behaving non-trivially; the other methods are unused
// by the validator path.
type stubMemoryReadonlyState struct {
	entries state.PodResourceEntries
}

func (s *stubMemoryReadonlyState) GetMachineState() state.NUMANodeResourcesMap { return nil }
func (s *stubMemoryReadonlyState) GetNUMAHeadroom() map[int]int64              { return nil }
func (s *stubMemoryReadonlyState) GetPodResourceEntries() state.PodResourceEntries {
	return s.entries
}

func (s *stubMemoryReadonlyState) GetAllocationInfo(_ v1.ResourceName, _, _ string) *state.AllocationInfo {
	return nil
}

func (s *stubMemoryReadonlyState) GetResourceAllocationInfo(_, _ string) map[v1.ResourceName]*state.AllocationInfo {
	return nil
}
func (s *stubMemoryReadonlyState) GetMachineInfo() *info.MachineInfo          { return nil }
func (s *stubMemoryReadonlyState) GetMemoryTopology() *machine.MemoryTopology { return nil }
func (s *stubMemoryReadonlyState) GetReservedMemory() map[v1.ResourceName]map[int]uint64 {
	return nil
}

// stubMemoryPoolsProvider is a minimal ResourcePoolsProvider for tests.
type stubMemoryPoolsProvider struct {
	pools map[int]map[string]nodev1alpha1.ResourcePool
}

func (s *stubMemoryPoolsProvider) NodeResourcePools(_ context.Context) (map[int]map[string]nodev1alpha1.ResourcePool, error) {
	return s.pools, nil
}

func newMemAI(pool string, agg uint64, perNUMA map[int]uint64) *state.AllocationInfo {
	annotations := map[string]string{}
	if pool != "" {
		annotations[apiconsts.PodAnnotationResourcePoolKey] = pool
	}
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			Annotations: annotations,
		},
		AggregatedQuantity:       agg,
		TopologyAwareAllocations: perNUMA,
	}
}

func memEntries(items map[string]map[string]*state.AllocationInfo) state.PodResourceEntries {
	return state.PodResourceEntries{
		v1.ResourceMemory: toPodEntries(items),
	}
}

func toPodEntries(items map[string]map[string]*state.AllocationInfo) state.PodEntries {
	out := state.PodEntries{}
	for podUID, containers := range items {
		ce := state.ContainerEntries{}
		for c, ai := range containers {
			ce[c] = ai
		}
		out[podUID] = ce
	}
	return out
}

func TestMemoryResourcePoolAllocatedProvider_NodeScope(t *testing.T) {
	t.Parallel()

	st := &stubMemoryReadonlyState{entries: memEntries(map[string]map[string]*state.AllocationInfo{
		"pod-a": {"c": newMemAI("p1", 2<<30, nil)}, // 2 GiB
		"pod-b": {"c": newMemAI("p1", 3<<30, nil)}, // 3 GiB
		"pod-c": {"c": newMemAI("p2", 4<<30, nil)}, // other pool, ignored
		"pod-d": {"c": newMemAI("", 100<<30, nil)}, // no pool, ignored
	})}
	p := newMemoryResourcePoolAllocatedProvider(st)

	got, err := p.GetAllocated("p1", rpvalidator.NodeScope())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := *resource.NewQuantity(int64(5)<<30, resource.BinarySI)
	q := got[v1.ResourceMemory]
	if q.Cmp(want) != 0 {
		t.Fatalf("expected 5 GiB, got %s", q.String())
	}
}

func TestMemoryResourcePoolAllocatedProvider_NumaScope(t *testing.T) {
	t.Parallel()

	st := &stubMemoryReadonlyState{entries: memEntries(map[string]map[string]*state.AllocationInfo{
		"pod-a": {"c": newMemAI("p1", 4<<30, map[int]uint64{
			0: 1 << 30,
			1: 3 << 30,
		})},
		"pod-b": {"c": newMemAI("p1", 2<<30, map[int]uint64{
			0: 2 << 30,
		})},
		"pod-c": {"c": newMemAI("p2", 100<<30, map[int]uint64{
			0: 100 << 30,
		})}, // other pool, ignored
	})}
	p := newMemoryResourcePoolAllocatedProvider(st)

	got0, err := p.GetAllocated("p1", rpvalidator.NumaScope(0))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	q0 := got0[v1.ResourceMemory]
	want0 := *resource.NewQuantity(int64(3)<<30, resource.BinarySI)
	if q0.Cmp(want0) != 0 {
		t.Fatalf("expected 3 GiB on numa-0, got %s", q0.String())
	}

	got1, err := p.GetAllocated("p1", rpvalidator.NumaScope(1))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	q1 := got1[v1.ResourceMemory]
	want1 := *resource.NewQuantity(int64(3)<<30, resource.BinarySI)
	if q1.Cmp(want1) != 0 {
		t.Fatalf("expected 3 GiB on numa-1, got %s", q1.String())
	}
}

func TestMemoryResourcePoolAllocatedProvider_EmptyPool(t *testing.T) {
	t.Parallel()

	st := &stubMemoryReadonlyState{entries: state.PodResourceEntries{}}
	p := newMemoryResourcePoolAllocatedProvider(st)

	got, err := p.GetAllocated("", rpvalidator.NodeScope())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty pool, got %v", got)
	}
}

// helper to build a *DynamicPolicy minimally for validateResourcePool tests.
func newMemoryPolicyWithValidator(pools map[int]map[string]nodev1alpha1.ResourcePool, st state.ReadonlyState) *DynamicPolicy {
	p := &DynamicPolicy{}
	p.resourcePoolValidator = rpvalidator.NewValidator(
		&stubMemoryPoolsProvider{pools: pools},
		newMemoryResourcePoolAllocatedProvider(st),
	)
	return p
}

func memMustListPtr(items map[v1.ResourceName]string) *v1.ResourceList {
	rl := v1.ResourceList{}
	for k, v := range items {
		rl[k] = resource.MustParse(v)
	}
	return &rl
}

func TestValidateResourcePool_Memory_NoPoolAnnotation(t *testing.T) {
	t.Parallel()

	p := newMemoryPolicyWithValidator(map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: memMustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "1Gi"})}},
	}, &stubMemoryReadonlyState{entries: state.PodResourceEntries{}})

	err := p.validateResourcePool(context.Background(), &pluginapi.ResourceRequest{Annotations: map[string]string{}}, map[v1.ResourceName]int{
		v1.ResourceMemory: 100 << 30,
	})
	if err != nil {
		t.Fatalf("expected nil err for pod without resource pool, got %v", err)
	}
}

func TestValidateResourcePool_Memory_NodeExceeded(t *testing.T) {
	t.Parallel()

	st := &stubMemoryReadonlyState{entries: memEntries(map[string]map[string]*state.AllocationInfo{
		"pod-a": {"c": newMemAI("p1", 8<<30, nil)},
	})}
	p := newMemoryPolicyWithValidator(map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: memMustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "10Gi"})}},
	}, st)

	req := &pluginapi.ResourceRequest{Annotations: map[string]string{
		apiconsts.PodAnnotationResourcePoolKey: "p1",
	}}
	// already 8Gi, incoming 3Gi -> 11Gi > 10Gi
	err := p.validateResourcePool(context.Background(), req, map[v1.ResourceName]int{
		v1.ResourceMemory: 3 << 30,
	})
	if !rpvalidator.IsCapacityExceeded(err) {
		t.Fatalf("expected capacity exceeded, got %v", err)
	}
}

func TestValidateResourcePool_Memory_Ok(t *testing.T) {
	t.Parallel()

	st := &stubMemoryReadonlyState{entries: state.PodResourceEntries{}}
	p := newMemoryPolicyWithValidator(map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: memMustListPtr(map[v1.ResourceName]string{v1.ResourceMemory: "10Gi"})}},
	}, st)

	req := &pluginapi.ResourceRequest{Annotations: map[string]string{
		apiconsts.PodAnnotationResourcePoolKey: "p1",
	}}
	if err := p.validateResourcePool(context.Background(), req, map[v1.ResourceName]int{
		v1.ResourceMemory: 5 << 30,
	}); err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
}

// TestValidateResourcePool_Memory_HugePagesNodeExceeded verifies that the
// NodeScope entry-level guard now rejects hugepage requests that would push
// the resource pool past its configured MaxAllocatable, mirroring the
// hint-phase NumaScope mask filter coverage.
func TestValidateResourcePool_Memory_HugePagesNodeExceeded(t *testing.T) {
	t.Parallel()

	hugepages2Mi := v1.ResourceName("hugepages-2Mi")
	st := &stubMemoryReadonlyState{entries: state.PodResourceEntries{
		hugepages2Mi: toPodEntries(map[string]map[string]*state.AllocationInfo{
			"pod-a": {"c": newMemAI("p1", 8<<20, nil)}, // 8 MiB hugepages already allocated
		}),
	}}
	p := newMemoryPolicyWithValidator(map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: memMustListPtr(map[v1.ResourceName]string{
			v1.ResourceMemory: "100Gi",
			hugepages2Mi:      "10Mi",
		})}},
	}, st)

	req := &pluginapi.ResourceRequest{Annotations: map[string]string{
		apiconsts.PodAnnotationResourcePoolKey: "p1",
	}}
	// already 8Mi hugepages, incoming 4Mi -> 12Mi > 10Mi
	err := p.validateResourcePool(context.Background(), req, map[v1.ResourceName]int{
		v1.ResourceMemory: 1 << 30,
		hugepages2Mi:      4 << 20,
	})
	if !rpvalidator.IsCapacityExceeded(err) {
		t.Fatalf("expected capacity exceeded for hugepages, got %v", err)
	}
}

// TestMemoryResourcePoolAllocatedProvider_HugePages verifies that the
// AllocatedProvider aggregates all resource names recorded in
// PodResourceEntries (memory + hugepages-*), so the hint-phase mask filter
// can validate hugepage capacity uniformly.
func TestMemoryResourcePoolAllocatedProvider_HugePages(t *testing.T) {
	t.Parallel()

	hugepages2Mi := v1.ResourceName("hugepages-2Mi")
	st := &stubMemoryReadonlyState{entries: state.PodResourceEntries{
		v1.ResourceMemory: toPodEntries(map[string]map[string]*state.AllocationInfo{
			"pod-a": {"c": newMemAI("p1", 4<<30, map[int]uint64{
				0: 4 << 30,
			})},
		}),
		hugepages2Mi: toPodEntries(map[string]map[string]*state.AllocationInfo{
			"pod-a": {"c": newMemAI("p1", 1024, map[int]uint64{
				0: 1024,
			})},
		}),
	}}
	p := newMemoryResourcePoolAllocatedProvider(st)

	got, err := p.GetAllocated("p1", rpvalidator.NumaScope(0))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	gotMem := got[v1.ResourceMemory]
	if gotMem.Cmp(*resource.NewQuantity(int64(4)<<30, resource.BinarySI)) != 0 {
		t.Fatalf("unexpected memory: %s", gotMem.String())
	}
	gotHuge := got[hugepages2Mi]
	if gotHuge.Cmp(*resource.NewQuantity(1024, resource.BinarySI)) != 0 {
		t.Fatalf("unexpected hugepages-2Mi: %s", gotHuge.String())
	}
}
