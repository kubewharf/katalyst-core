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

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	nodev1alpha1 "github.com/kubewharf/katalyst-api/pkg/apis/node/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/state"
	rpvalidator "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/resourcepoolvalidator"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
	resourcepool "github.com/kubewharf/katalyst-core/pkg/util/resource-pool"
)

// stubReadonlyState implements state.ReadonlyState with only GetPodEntries
// behaving non-trivially; the other methods are unused by the validator path.
type stubReadonlyState struct {
	entries state.PodEntries
}

func (s *stubReadonlyState) GetMachineState() state.NUMANodeMap             { return nil }
func (s *stubReadonlyState) GetNUMAHeadroom() map[int]float64               { return nil }
func (s *stubReadonlyState) GetPodEntries() state.PodEntries                { return s.entries }
func (s *stubReadonlyState) GetAllowSharedCoresOverlapReclaimedCores() bool { return false }
func (s *stubReadonlyState) GetAllocationInfo(_ string, _ string) *state.AllocationInfo {
	return nil
}

// stubPoolsProvider is a minimal ResourcePoolsProvider for tests.
type stubPoolsProvider struct {
	pools map[int]map[string]nodev1alpha1.ResourcePool
}

func (s *stubPoolsProvider) NodeResourcePools(_ context.Context) (map[int]map[string]nodev1alpha1.ResourcePool, error) {
	return s.pools, nil
}

func newAI(pool string, request float64, assignments map[int]machine.CPUSet) *state.AllocationInfo {
	annotations := map[string]string{}
	if pool != "" {
		annotations[apiconsts.PodAnnotationResourcePoolKey] = pool
	}
	return &state.AllocationInfo{
		AllocationMeta: commonstate.AllocationMeta{
			Annotations: annotations,
		},
		RequestQuantity:          request,
		TopologyAwareAssignments: assignments,
	}
}

// newDNBAI builds a DNB (dedicated_cores + numa_binding) AllocationInfo for
// per-NUMA cpuset pro-rate accounting tests.
func newDNBAI(pool string, request float64, assignments map[int]machine.CPUSet) *state.AllocationInfo {
	ai := newAI(pool, request, assignments)
	ai.AllocationMeta.QoSLevel = apiconsts.PodAnnotationQoSLevelDedicatedCores
	ai.AllocationMeta.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] = apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable
	return ai
}

// newSNBAI builds a SNB (shared_cores + numa_binding) AllocationInfo bound to
// targetNUMA via the persisted CPUStateAnnotationKeyNUMAHint annotation.
func newSNBAI(pool string, request float64, targetNUMA int, assignments map[int]machine.CPUSet) *state.AllocationInfo {
	ai := newAI(pool, request, assignments)
	ai.AllocationMeta.QoSLevel = apiconsts.PodAnnotationQoSLevelSharedCores
	ai.AllocationMeta.Annotations[apiconsts.PodAnnotationMemoryEnhancementNumaBinding] = apiconsts.PodAnnotationMemoryEnhancementNumaBindingEnable
	ai.AllocationMeta.SetSpecifiedNUMABindingNUMAID([]uint64{uint64(targetNUMA)})
	return ai
}

func TestCPUResourcePoolAllocatedProvider_NodeScope(t *testing.T) {
	t.Parallel()

	st := &stubReadonlyState{entries: state.PodEntries{
		"pod-a": {"c": newAI("p1", 2, nil)},
		"pod-b": {"c": newAI("p1", 3.5, nil)},
		"pod-c": {"c": newAI("p2", 4, nil)},
		"pod-d": {"c": newAI("", 100, nil)}, // no pool annotation -> ignored
	}}
	p := newCPUResourcePoolAllocatedProvider(st)

	got, err := p.GetAllocated("p1", rpvalidator.NodeScope())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	want := *resource.NewMilliQuantity(int64(5500), resource.DecimalSI)
	q := got[v1.ResourceCPU]
	if q.Cmp(want) != 0 {
		t.Fatalf("expected 5500m, got %s", q.String())
	}
}

func TestCPUResourcePoolAllocatedProvider_NumaScope(t *testing.T) {
	t.Parallel()

	st := &stubReadonlyState{entries: state.PodEntries{
		// DNB: 4 cpu request, evenly split across numa-0 and numa-1: each numa contributes 2.
		"pod-a": {"c": newDNBAI("p1", 4, map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1),
			1: machine.NewCPUSet(2, 3),
		})},
		// DNB: 2 cpu, all on numa-0.
		"pod-b": {"c": newDNBAI("p1", 2, map[int]machine.CPUSet{
			0: machine.NewCPUSet(4, 5),
		})},
		// DNB but in another pool -> ignored.
		"pod-c": {"c": newDNBAI("p2", 100, map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1),
		})},
	}}
	p := newCPUResourcePoolAllocatedProvider(st)

	got0, err := p.GetAllocated("p1", rpvalidator.NumaScope(0))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	q0 := got0[v1.ResourceCPU]
	want0 := *resource.NewMilliQuantity(4000, resource.DecimalSI)
	if q0.Cmp(want0) != 0 {
		t.Fatalf("expected 4000m on numa-0, got %s", q0.String())
	}

	got1, err := p.GetAllocated("p1", rpvalidator.NumaScope(1))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	q1 := got1[v1.ResourceCPU]
	want1 := *resource.NewMilliQuantity(2000, resource.DecimalSI)
	if q1.Cmp(want1) != 0 {
		t.Fatalf("expected 2000m on numa-1, got %s", q1.String())
	}
}

// TestCPUResourcePoolAllocatedProvider_NumaScope_SNB verifies that SNB
// containers contribute the entire RequestQuantity against their persisted
// target NUMA hint, and nothing on any other NUMA — even if their
// TopologyAwareAssignments temporarily span multiple NUMAs (ramp-up).
func TestCPUResourcePoolAllocatedProvider_NumaScope_SNB(t *testing.T) {
	t.Parallel()

	st := &stubReadonlyState{entries: state.PodEntries{
		// SNB bound to numa-3 but cpuset still spans numa-3 and numa-2 (ramp-up).
		"pod-a": {"c": newSNBAI("p1", 2, 3, map[int]machine.CPUSet{
			3: machine.NewCPUSet(12, 13),
			2: machine.NewCPUSet(8, 9),
		})},
	}}
	p := newCPUResourcePoolAllocatedProvider(st)

	got3, err := p.GetAllocated("p1", rpvalidator.NumaScope(3))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	q3 := got3[v1.ResourceCPU]
	want3 := *resource.NewMilliQuantity(2000, resource.DecimalSI)
	if q3.Cmp(want3) != 0 {
		t.Fatalf("expected 2000m on target numa-3, got %s", q3.String())
	}

	got2, err := p.GetAllocated("p1", rpvalidator.NumaScope(2))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	q2 := got2[v1.ResourceCPU]
	want2 := *resource.NewMilliQuantity(0, resource.DecimalSI)
	if q2.Cmp(want2) != 0 {
		t.Fatalf("expected 0m on non-target numa-2 (ramp-up cpuset must not leak), got %s", q2.String())
	}
}

// TestCPUResourcePoolAllocatedProvider_NumaScope_NonSNBNonDNB verifies that
// containers without SNB/DNB binding semantics (e.g. plain shared_cores
// without numa_binding) do not contribute to per-NUMA accounting.
func TestCPUResourcePoolAllocatedProvider_NumaScope_NonSNBNonDNB(t *testing.T) {
	t.Parallel()

	st := &stubReadonlyState{entries: state.PodEntries{
		"pod-a": {"c": newAI("p1", 4, map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1),
			1: machine.NewCPUSet(2, 3),
		})},
	}}
	p := newCPUResourcePoolAllocatedProvider(st)

	for _, numaID := range []int{0, 1} {
		got, err := p.GetAllocated("p1", rpvalidator.NumaScope(numaID))
		if err != nil {
			t.Fatalf("unexpected err on numa-%d: %v", numaID, err)
		}
		q := got[v1.ResourceCPU]
		if q.Cmp(*resource.NewMilliQuantity(0, resource.DecimalSI)) != 0 {
			t.Fatalf("expected 0m on numa-%d for non-SNB/non-DNB container, got %s", numaID, q.String())
		}
	}
}

func TestCPUResourcePoolAllocatedProvider_EmptyPool(t *testing.T) {
	t.Parallel()

	st := &stubReadonlyState{entries: state.PodEntries{}}
	p := newCPUResourcePoolAllocatedProvider(st)

	got, err := p.GetAllocated("", rpvalidator.NodeScope())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil for empty pool, got %v", got)
	}
}

// helper to build a *DynamicPolicy minimally for validateResourcePool tests.
func newPolicyWithValidator(pools map[int]map[string]nodev1alpha1.ResourcePool, st state.ReadonlyState) *DynamicPolicy {
	p := &DynamicPolicy{}
	p.resourcePoolValidator = rpvalidator.NewValidator(
		&stubPoolsProvider{pools: pools},
		newCPUResourcePoolAllocatedProvider(st),
	)
	return p
}

func mustListPtr(items map[v1.ResourceName]string) *v1.ResourceList {
	rl := v1.ResourceList{}
	for k, v := range items {
		rl[k] = resource.MustParse(v)
	}
	return &rl
}

func TestValidateResourcePool_CPU_NoPoolAnnotation(t *testing.T) {
	t.Parallel()

	p := newPolicyWithValidator(map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "1"})}},
	}, &stubReadonlyState{entries: state.PodEntries{}})

	err := p.validateResourcePool(context.Background(), &pluginapi.ResourceRequest{Annotations: map[string]string{}}, 100)
	if err != nil {
		t.Fatalf("expected nil err for pod without resource pool, got %v", err)
	}
}

func TestValidateResourcePool_CPU_NodeExceeded(t *testing.T) {
	t.Parallel()

	st := &stubReadonlyState{entries: state.PodEntries{
		"pod-a": {"c": newAI("p1", 8, nil)},
	}}
	p := newPolicyWithValidator(map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}, st)

	req := &pluginapi.ResourceRequest{Annotations: map[string]string{
		apiconsts.PodAnnotationResourcePoolKey: "p1",
	}}
	// already 8, incoming 3 -> 11 > 10
	err := p.validateResourcePool(context.Background(), req, 3)
	if !rpvalidator.IsCapacityExceeded(err) {
		t.Fatalf("expected capacity exceeded, got %v", err)
	}
}

func TestValidateResourcePool_CPU_Ok(t *testing.T) {
	t.Parallel()

	st := &stubReadonlyState{entries: state.PodEntries{}}
	p := newPolicyWithValidator(map[int]map[string]nodev1alpha1.ResourcePool{
		resourcepool.NumaIDAll: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "10"})}},
	}, st)

	req := &pluginapi.ResourceRequest{Annotations: map[string]string{
		apiconsts.PodAnnotationResourcePoolKey: "p1",
	}}
	if err := p.validateResourcePool(context.Background(), req, 5); err != nil {
		t.Fatalf("expected nil err, got %v", err)
	}
}

// TestCPUResourcePoolMaskExceeds_NoAnnotation verifies that requests without
// a resource pool annotation are not filtered by the mask-level validator.
func TestCPUResourcePoolMaskExceeds_NoAnnotation(t *testing.T) {
	t.Parallel()

	st := &stubReadonlyState{entries: state.PodEntries{}}
	p := newPolicyWithValidator(map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "1"})}},
	}, st)

	req := &pluginapi.ResourceRequest{Annotations: map[string]string{}}
	if p.cpuResourcePoolMaskExceeds(context.Background(), req, 100, []int{0}, nil, false) {
		t.Fatalf("expected pod without pool annotation to be unfiltered")
	}
}

// TestCPUResourcePoolMaskExceeds_SingleNUMAExceeded verifies that a single
// NUMA mask whose share would exceed pool MaxAllocatable returns true.
func TestCPUResourcePoolMaskExceeds_SingleNUMAExceeded(t *testing.T) {
	t.Parallel()

	st := &stubReadonlyState{entries: state.PodEntries{
		"pod-a": {"c": newDNBAI("p1", 3, map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1, 2),
		})},
	}}
	pools := map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
	}
	p := newPolicyWithValidator(pools, st)

	req := &pluginapi.ResourceRequest{Annotations: map[string]string{
		apiconsts.PodAnnotationResourcePoolKey: "p1",
	}}
	// already 3, incoming 2 on numa-0 -> 5 > 4 -> exceeded.
	if !p.cpuResourcePoolMaskExceeds(context.Background(), req, 2, []int{0}, nil, false) {
		t.Fatalf("expected mask filter to reject single-numa overflow")
	}
}

// TestCPUResourcePoolMaskExceeds_MultiNUMAAggregated verifies that the
// total request is validated against the aggregated capacity across NUMAs.
func TestCPUResourcePoolMaskExceeds_MultiNUMAAggregated(t *testing.T) {
	t.Parallel()

	st := &stubReadonlyState{entries: state.PodEntries{
		"pod-a": {"c": newDNBAI("p1", 6, map[int]machine.CPUSet{
			0: machine.NewCPUSet(0, 1, 2),
			1: machine.NewCPUSet(3, 4, 5),
		})},
	}}
	pools := map[int]map[string]nodev1alpha1.ResourcePool{
		0: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
		1: {"p1": {PoolName: "p1", MaxAllocatable: mustListPtr(map[v1.ResourceName]string{v1.ResourceCPU: "4"})}},
	}
	p := newPolicyWithValidator(pools, st)

	req := &pluginapi.ResourceRequest{Annotations: map[string]string{
		apiconsts.PodAnnotationResourcePoolKey: "p1",
	}}
	// allocated 3 per numa (total 6), max 4+4=8, total request 2 -> 6+2=8 <= 8 -> ok.
	if p.cpuResourcePoolMaskExceeds(context.Background(), req, 2, []int{0, 1}, nil, false) {
		t.Fatalf("expected total request to fit within aggregated capacity")
	}

	// allocated 3 per numa (total 6), max 4+4=8, total request 3 -> 6+3=9 > 8 -> exceeded.
	if !p.cpuResourcePoolMaskExceeds(context.Background(), req, 3, []int{0, 1}, nil, false) {
		t.Fatalf("expected total request to exceed aggregated capacity")
	}
}
