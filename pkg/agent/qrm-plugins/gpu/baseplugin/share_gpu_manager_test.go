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

package baseplugin

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	workloadapis "github.com/kubewharf/katalyst-api/pkg/apis/workload/v1alpha1"
	apiconsts "github.com/kubewharf/katalyst-api/pkg/consts"
	commonstate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/commonstate"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	gpustate "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metaserver/spd"
)

// fakeSPM is a lightweight ServiceProfilingManager for tests.
type fakeSPM struct {
	behaviors map[types.UID]struct {
		baseline bool
		enable   *bool
		err      error
	}
	calls int
}

func (f *fakeSPM) ServiceExtendedIndicator(_ context.Context, podMeta metav1.ObjectMeta, indicators interface{}) (bool, error) {
	f.calls++
	b := f.behaviors[podMeta.UID]
	// fill EnableShareGPU if indicators is of proper type
	if ind, ok := indicators.(*v1alpha1.ReclaimResourceIndicators); ok {
		ind.EnableShareGPU = b.enable
	}
	if b.err != nil {
		return false, b.err
	}
	return b.baseline, nil
}

// Stubs for unused interface methods
func (f *fakeSPM) ServiceBusinessPerformanceLevel(context.Context, metav1.ObjectMeta) (spd.PerformanceLevel, error) {
	return spd.PerformanceLevelPerfect, nil
}

func (f *fakeSPM) ServiceBusinessPerformanceScore(context.Context, metav1.ObjectMeta) (float64, error) {
	return spd.MaxPerformanceScore, nil
}

func (f *fakeSPM) ServiceSystemPerformanceTarget(context.Context, metav1.ObjectMeta) (spd.IndicatorTarget, error) {
	return spd.IndicatorTarget{}, nil
}

func (f *fakeSPM) ServiceBaseline(context.Context, metav1.ObjectMeta) (bool, error) {
	return false, nil
}

func (f *fakeSPM) ServiceAggregateMetrics(context.Context, metav1.ObjectMeta, v1.ResourceName, bool, workloadapis.Aggregator, workloadapis.Aggregator) ([]resource.Quantity, error) {
	return nil, nil
}
func (f *fakeSPM) Run(context.Context) {}

// fakeState provides minimal state for tests.
type fakeState struct {
	machine gpustate.AllocationResourcesMap
}

func (f *fakeState) AddMachineStateSyncNotifier(notifier func()) {}

func (f *fakeState) GetMachineState() gpustate.AllocationResourcesMap {
	return f.machine
}
func (f *fakeState) GetPodResourceEntries() gpustate.PodResourceEntries             { return nil }
func (f *fakeState) GetPodEntries(resourceName v1.ResourceName) gpustate.PodEntries { return nil }
func (f *fakeState) GetAllocationInfo(resourceName v1.ResourceName, podUID, containerName string) *gpustate.AllocationInfo {
	return nil
}
func (f *fakeState) SetMachineState(gpustate.AllocationResourcesMap, bool)          {}
func (f *fakeState) SetResourceState(v1.ResourceName, gpustate.AllocationMap, bool) {}
func (f *fakeState) SetPodResourceEntries(gpustate.PodResourceEntries, bool)        {}
func (f *fakeState) SetAllocationInfo(v1.ResourceName, string, string, *gpustate.AllocationInfo, bool) {
}
func (f *fakeState) Delete(v1.ResourceName, string, string, bool) {}
func (f *fakeState) ClearState()                                  {}
func (f *fakeState) StoreState() error                            { return nil }

func makeContainer(podUID, podNS, podName, containerName string, main bool, ids ...string) *gpustate.AllocationInfo {
	t := pluginapi.ContainerType_SIDECAR.String()
	if main {
		t = pluginapi.ContainerType_MAIN.String()
	}
	topo := make(map[string]gpustate.Allocation)
	for _, id := range ids {
		topo[id] = gpustate.Allocation{Quantity: 1}
	}
	return &gpustate.AllocationInfo{
		AllocationMeta: gpustate.AllocationInfo{AllocationMeta: commonstate.AllocationMeta{
			PodUid:        podUID,
			PodNamespace:  podNS,
			PodName:       podName,
			ContainerName: containerName,
			ContainerType: t,
		}}.AllocationMeta,
		AllocatedAllocation:      gpustate.Allocation{Quantity: 1},
		TopologyAwareAllocations: topo,
	}
}

func Test_EnableShareGPU_DefaultFalse(t *testing.T) {
	t.Parallel()
	m := NewShareGPUManager(nil, []string{gpuconsts.GPUDeviceType}).(*shareGPUManager)
	got := m.EnableShareGPU(gpuconsts.GPUDeviceType, "non-existent")
	if got == nil {
		t.Fatalf("expected non-nil decision when resourceName is configured")
	}
	if *got {
		t.Fatalf("expected false for unknown id, got true")
	}

	// resourceName not configured -> nil (caller should not adjust device behavior).
	if v := m.EnableShareGPU("unknown-resource", "non-existent"); v != nil {
		t.Fatalf("expected nil for unconfigured resourceName, got %v", *v)
	}
}

func Test_EnableShareGPU_NilWhenResourceNotConfigured(t *testing.T) {
	t.Parallel()
	m := NewShareGPUManager(nil, nil).(*shareGPUManager)
	if v := m.EnableShareGPU(gpuconsts.GPUDeviceType, "GPU-1"); v != nil {
		t.Fatalf("expected nil when ShareGPUResourceNames is empty, got %v", *v)
	}
}

func Test_PreAllocate_ResourceNotConfigured(t *testing.T) {
	t.Parallel()
	m := NewShareGPUManager(nil, nil).(*shareGPUManager)
	// Even with an explicit unshareable mark, when resourceName is not
	// configured PreAllocate must be a no-op.
	m.shareGPUMap["GPU-1"] = false

	deviceReq := &pluginapi.DeviceRequest{
		DeviceName:       gpuconsts.GPUDeviceType,
		AvailableDevices: []string{"GPU-1", "GPU-2"},
	}
	m.PreAllocate(context.Background(), &pluginapi.ResourceRequest{}, deviceReq)
	if !equalStringSlice(deviceReq.AvailableDevices, []string{"GPU-1", "GPU-2"}) {
		t.Fatalf("expected AvailableDevices unchanged, got %v", deviceReq.AvailableDevices)
	}
}

func Test_PreAllocate_FiltersUnshareable(t *testing.T) {
	t.Parallel()
	m := NewShareGPUManager(nil, []string{gpuconsts.GPUDeviceType}).(*shareGPUManager)
	m.shareGPUMap["GPU-1"] = false
	m.shareGPUMap["GPU-2"] = true

	deviceReq := &pluginapi.DeviceRequest{
		DeviceName:       gpuconsts.GPUDeviceType,
		AvailableDevices: []string{"GPU-1", "GPU-2", "GPU-3"},
	}
	m.PreAllocate(context.Background(), &pluginapi.ResourceRequest{}, deviceReq)
	if !equalStringSlice(deviceReq.AvailableDevices, []string{"GPU-2", "GPU-3"}) {
		t.Fatalf("expected [GPU-2 GPU-3], got %v", deviceReq.AvailableDevices)
	}
}

func Test_PreAllocate_NilDeviceReq(t *testing.T) {
	t.Parallel()
	m := NewShareGPUManager(nil, []string{gpuconsts.GPUDeviceType}).(*shareGPUManager)
	// Should not panic.
	m.PreAllocate(context.Background(), &pluginapi.ResourceRequest{}, nil)
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func Test_Allocate_MarkFalse_OnIndicatorFalse(t *testing.T) {
	t.Parallel()
	spm := &fakeSPM{behaviors: map[types.UID]struct {
		baseline bool
		enable   *bool
		err      error
	}{
		types.UID("u1"): {baseline: false, enable: ptrBool(false)},
	}}
	bp := &BasePlugin{MetaServer: &metaserver.MetaServer{ServiceProfilingManager: spm}}
	m := NewShareGPUManager(nil, []string{gpuconsts.GPUDeviceType}).(*shareGPUManager)
	m.basePlugin = bp

	alloc := makeContainer("u1", "ns", "pod", "c", true, "GPU-1", "GPU-2")
	m.PostAllocate(context.Background(), alloc)

	v1 := m.EnableShareGPU(gpuconsts.GPUDeviceType, "GPU-1")
	v2 := m.EnableShareGPU(gpuconsts.GPUDeviceType, "GPU-2")
	if v1 == nil || v2 == nil {
		t.Fatalf("expected non-nil decisions when resourceName is configured")
	}
	if *v1 || *v2 {
		t.Fatalf("expected devices marked false after Allocate")
	}
}

func Test_Sync_BuildsSnapshot_WithCache(t *testing.T) {
	t.Parallel()
	enable := ptrBool(true)
	spm := &fakeSPM{behaviors: map[types.UID]struct {
		baseline bool
		enable   *bool
		err      error
	}{
		types.UID("u1"): {baseline: false, enable: enable},
	}}
	bp := &BasePlugin{MetaServer: &metaserver.MetaServer{ServiceProfilingManager: spm}}

	// Build machine state: one device with two main containers of same pod
	ce := gpustate.ContainerEntries{
		"c1": makeContainer("u1", "ns", "pod", "c1", true, "GPU-1"),
		"c2": makeContainer("u1", "ns", "pod", "c2", true, "GPU-1"),
	}
	allocState := &gpustate.AllocationState{PodEntries: gpustate.PodEntries{"u1": ce}}
	machine := gpustate.AllocationResourcesMap{v1.ResourceName(gpuconsts.GPUDeviceType): gpustate.AllocationMap{"GPU-1": allocState}}

	m := NewShareGPUManager(nil, []string{gpuconsts.GPUDeviceType}).(*shareGPUManager)
	m.basePlugin = &BasePlugin{MetaServer: bp.MetaServer}
	m.basePlugin.SetState(&fakeState{machine: machine})
	m.sync(context.Background())

	v := m.EnableShareGPU(gpuconsts.GPUDeviceType, "GPU-1")
	if v == nil || !*v {
		t.Fatalf("expected GPU-1 shareable")
	}
	if spm.calls != 1 {
		t.Fatalf("expected 1 indicator call due to cache, got %d", spm.calls)
	}
}

func Test_Concurrency_Safety(t *testing.T) {
	t.Parallel()
	enable := ptrBool(true)
	spm := &fakeSPM{behaviors: map[types.UID]struct {
		baseline bool
		enable   *bool
		err      error
	}{
		types.UID("u1"): {baseline: false, enable: enable},
	}}
	bp := &BasePlugin{MetaServer: &metaserver.MetaServer{ServiceProfilingManager: spm}}
	m := NewShareGPUManager(nil, []string{gpuconsts.GPUDeviceType}).(*shareGPUManager)
	m.basePlugin = bp

	alloc := makeContainer("u1", "ns", "pod", "c", true, "GPU-1")

	var wg sync.WaitGroup
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Writer goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			m.PostAllocate(ctx, alloc)
			time.Sleep(time.Millisecond)
		}
	}()

	// Reader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			_ = m.EnableShareGPU(gpuconsts.GPUDeviceType, "GPU-1")
			time.Sleep(time.Millisecond)
		}
	}()

	wg.Wait()
}

func Test_Run_StartsAndStops(t *testing.T) {
	t.Parallel()
	enable := ptrBool(true)
	spm := &fakeSPM{behaviors: map[types.UID]struct {
		baseline bool
		enable   *bool
		err      error
	}{
		types.UID("u1"): {baseline: false, enable: enable},
	}}
	bp := &BasePlugin{MetaServer: &metaserver.MetaServer{ServiceProfilingManager: spm}}
	bp.SetState(&fakeState{machine: gpustate.AllocationResourcesMap{gpuconsts.GPUDeviceType: gpustate.AllocationMap{"GPU-1": &gpustate.AllocationState{PodEntries: gpustate.PodEntries{"u1": gpustate.ContainerEntries{"c": makeContainer("u1", "ns", "pod", "c", true, "GPU-1")}}}}}})
	m := NewShareGPUManager(bp, []string{gpuconsts.GPUDeviceType})

	stopCh := make(chan struct{})
	m.Run(stopCh)
	time.Sleep(50 * time.Millisecond)
	close(stopCh)
}

func Test_Allocate_Ignores_NonMain(t *testing.T) {
	t.Parallel()
	spm := &fakeSPM{behaviors: map[types.UID]struct {
		baseline bool
		enable   *bool
		err      error
	}{}}
	bp := &BasePlugin{MetaServer: &metaserver.MetaServer{ServiceProfilingManager: spm}}
	m := NewShareGPUManager(nil, []string{gpuconsts.GPUDeviceType}).(*shareGPUManager)
	m.basePlugin = bp

	alloc := makeContainer("u2", "ns", "pod", "c", false, "GPU-1")
	m.PostAllocate(context.Background(), alloc)
	v := m.EnableShareGPU(gpuconsts.GPUDeviceType, "GPU-1")
	if v == nil {
		t.Fatalf("expected non-nil decision when resourceName is configured")
	}
	if *v {
		t.Fatalf("non-main container should not mark device")
	}
}

func Test_evaluateDeviceShareStatus_Ignores_reclaimed(t *testing.T) {
	t.Parallel()
	m := NewShareGPUManager(nil, nil).(*shareGPUManager)
	alloc := makeContainer("u2", "ns", "pod", "c", true, "GPU-1")
	alloc.QoSLevel = apiconsts.PodAnnotationQoSLevelReclaimedCores
	as := &gpustate.AllocationState{
		PodEntries: map[string]gpustate.ContainerEntries{
			"u2": {
				"c": alloc,
			},
		},
	}

	if !m.evaluateDeviceShareStatus(context.Background(), as, make(map[types.UID]bool)) {
		t.Fatalf("reclaimed container should not mark device")
	}
}

func ptrBool(b bool) *bool { v := b; return &v }

func BenchmarkSync_WithIndicatorCache(b *testing.B) {
	enable := ptrBool(true)
	spm := &fakeSPM{behaviors: map[types.UID]struct {
		baseline bool
		enable   *bool
		err      error
	}{
		types.UID("u1"): {baseline: false, enable: enable},
	}}
	bp := &BasePlugin{MetaServer: &metaserver.MetaServer{ServiceProfilingManager: spm}}

	// Build a large machine state: N devices, each with M main containers from the same pod
	const N, M = 50, 20
	am := make(gpustate.AllocationMap, N)
	for i := 0; i < N; i++ {
		ce := make(gpustate.ContainerEntries, M)
		for j := 0; j < M; j++ {
			cname := fmt.Sprintf("c%c", 'A'+j)
			ce[cname] = makeContainer("u1", "ns", "pod", cname, true, fmt.Sprintf("GPU-%c", 'A'+i))
		}
		am[fmt.Sprintf("GPU-%c", 'A'+i)] = &gpustate.AllocationState{PodEntries: gpustate.PodEntries{"u1": ce}}
	}
	machine := gpustate.AllocationResourcesMap{v1.ResourceName(gpuconsts.GPUDeviceType): am}

	m := NewShareGPUManager(nil, []string{gpuconsts.GPUDeviceType}).(*shareGPUManager)
	m.basePlugin = &BasePlugin{MetaServer: bp.MetaServer}
	m.basePlugin.SetState(&fakeState{machine: machine})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		spm.calls = 0
		m.sync(context.Background())
	}

	// Expect only 1 external call per sync due to cache (one unique pod UID)
	if spm.calls != 1 {
		b.Fatalf("expected 1 indicator call per sync, got %d", spm.calls)
	}
}
