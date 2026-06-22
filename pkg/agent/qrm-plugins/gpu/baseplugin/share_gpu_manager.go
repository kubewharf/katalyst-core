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
	"reflect"
	"sync"
	"time"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	pluginapi "k8s.io/kubelet/pkg/apis/resourceplugin/v1alpha1"

	"github.com/kubewharf/katalyst-api/pkg/apis/config/v1alpha1"
	gpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// This module determines whether each GPU device is eligible for shared usage
// (ShareGPU) based on per-pod extended indicators fetched from MetaServer. It
// periodically scans the GPU machine state maintained by BasePlugin and builds
// a snapshot map `shareGPUMap` that records the ShareGPU decision for each
// device ID.
// Decision rule: Only main containers are considered; if any main container on
// a device disables ShareGPU, the device is marked non-shareable. To reduce
// repeated external queries, a per-sync in-memory cache keyed by `pod UID` is
// used to memoize `EnableShareGPU` decisions.

// ShareGPUManager determines per-device ShareGPU eligibility and maintains a
// periodic snapshot. Safe for concurrent reads via `EnableShareGPU`.
//
// Time Complexity:
//   - `sync`: O(D + C) where D is number of devices and C is number of main
//     containers scanned; per-pod indicator lookups are amortized O(1) via cache.
//   - `EnableShareGPU`: O(1) map read.
//   - `Allocate`: O(A) over `TopologyAwareAllocations` entries when marking.
//
// Potential Errors:
// - External indicator fetch may fail; treated conservatively and wrapped.
// - Internal panics are recovered to keep the manager running.
type ShareGPUManager interface {
	// PreAllocate filters out unshareable device IDs from deviceReq.AvailableDevices
	// when deviceReq.DeviceName is configured under ShareGPUResourceNames.
	// The filtered list is written back to deviceReq.AvailableDevices in place.
	// For resources not in ShareGPUResourceNames, this is a no-op.
	// Params:
	//   - ctx: request-scoped context (reserved for future external lookups)
	//   - resReq: the original ResourceRequest (reserved for future pod-level decisions)
	//   - deviceReq: the device request whose AvailableDevices may be filtered in place
	PreAllocate(ctx context.Context, resReq *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest)

	// PostAllocate processes an allocation of a main container. If the pod's
	// indicator disables ShareGPU, all involved device IDs in
	// `TopologyAwareAllocations` are marked non-shareable in the snapshot.
	// Params:
	// - ctx: request-scoped context for external calls
	// - allocationInfo: container allocation metadata; ignored if nil or non-main
	// Returns: none; updates internal snapshot
	// Errors: any external errors are logged and wrapped internally
	PostAllocate(ctx context.Context, allocationInfo *state.AllocationInfo)

	// EnableShareGPU returns the cached ShareGPU decision for a given device ID.
	// Params:
	// - resourceName: the GPU resource name (e.g. "nvidia.com/gpu") that the device belongs to
	// - id: device ID string
	// Returns: a *bool pointing to the share decision when resourceName is in the configured
	//   ShareGPUResourceNames; nil when resourceName is not configured (callers should not
	//   adjust device behavior based on ShareGPU status in that case).
	EnableShareGPU(resourceName, id string) *bool

	// Run kicks off ShareGPU decision synchronization. The initial sync runs
	// synchronously to ensure a fresh snapshot is in place before Run returns;
	// subsequent periodic syncs are dispatched to a background goroutine so
	// the call does not block. The background loop exits when `stopCh` is closed.
	// Params:
	// - stopCh: channel to stop the background loop
	// Notes: non-blocking method; callers do not need to wrap it in `go ...`.
	Run(stopCh <-chan struct{})
}
type shareGPUManager struct {
	sync.RWMutex

	shareGPUMap           map[string]bool
	shareGPUResourceNames sets.String
	basePlugin            *BasePlugin
}

// NewShareGPUManager creates a new ShareGPUManager instance.
// Params:
//   - basePlugin: plugin providing machine state and MetaServer
//   - shareGPUResourceNames: GPU resource names that should participate in ShareGPU decisions; only
//     EnableShareGPU calls whose resourceName is in this list will return a non-nil decision.
//
// Returns: a manager with an empty snapshot cache.
func NewShareGPUManager(basePlugin *BasePlugin, shareGPUResourceNames []string) ShareGPUManager {
	return &shareGPUManager{
		shareGPUMap:           make(map[string]bool),
		shareGPUResourceNames: sets.NewString(shareGPUResourceNames...),
		basePlugin:            basePlugin,
	}
}

// EnableShareGPU returns the cached ShareGPU decision for a given device ID.
// If resourceName is not in the configured ShareGPUResourceNames, it returns nil and
// the caller should not make any ShareGPU-based adjustments for the device.
// If the device ID is not present in the latest snapshot, it returns a *bool pointing to false.
// Complexity: O(1) map lookup.
func (s *shareGPUManager) EnableShareGPU(resourceName, id string) *bool {
	if !s.shareGPUResourceNames.Has(resourceName) {
		return nil
	}

	s.RLock()
	defer s.RUnlock()

	v := s.shareGPUMap[id]
	return &v
}

// PreAllocate filters out unshareable device IDs from deviceReq.AvailableDevices
// when deviceReq.DeviceName is configured under ShareGPUResourceNames.
// The filtered list is written back to deviceReq.AvailableDevices in place.
// For resources not in ShareGPUResourceNames, this is a no-op.
// Complexity: O(N) where N is len(deviceReq.AvailableDevices).
func (s *shareGPUManager) PreAllocate(_ context.Context, _ *pluginapi.ResourceRequest, deviceReq *pluginapi.DeviceRequest) {
	if deviceReq == nil {
		return
	}
	if !s.shareGPUResourceNames.Has(deviceReq.DeviceName) {
		return
	}

	s.RLock()
	defer s.RUnlock()

	filtered := make([]string, 0, len(deviceReq.AvailableDevices))
	for _, id := range deviceReq.AvailableDevices {
		// Drop only devices explicitly marked false (unshareable) in the snapshot.
		// Unknown devices are kept to avoid false negatives between sync cycles.
		if v, ok := s.shareGPUMap[id]; ok && !v {
			continue
		}
		filtered = append(filtered, id)
	}
	deviceReq.AvailableDevices = filtered
}

// PostAllocate marks involved device IDs as non-shareable if the pod disables
// ShareGPU. Non-main containers are ignored.
// Complexity: O(A) where A is number of device IDs in TopologyAwareAllocations.
func (s *shareGPUManager) PostAllocate(ctx context.Context, allocationInfo *state.AllocationInfo) {
	if allocationInfo == nil || !allocationInfo.CheckMainContainer() || allocationInfo.CheckReclaimed() {
		return
	}

	enableShareGPU := s.evaluateContainerDeviceShareStatus(ctx, allocationInfo, nil)
	if enableShareGPU {
		return
	}

	s.Lock()
	defer s.Unlock()
	for id := range allocationInfo.TopologyAwareAllocations {
		s.shareGPUMap[id] = false
	}
}

// Run kicks off ShareGPU decision synchronization. The initial sync runs
// synchronously in the caller's goroutine to guarantee that a fresh snapshot
// is in place before Run returns (so subsequent EnableShareGPU/PreAllocate
// calls observe up-to-date data immediately). The periodic 30s loop is then
// dispatched to a background goroutine so the method returns without blocking.
// The background loop exits when `stopCh` is closed.
// Params:
// - stopCh: channel to stop the background loop
// Note: Non-blocking — callers should NOT wrap this call in `go ...`.
func (s *shareGPUManager) Run(stopCh <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		<-stopCh
		cancel()
	}()

	// Initial sync runs synchronously to ensure readiness before returning.
	s.sync(ctx)

	// Periodic sync runs in the background so Run is non-blocking.
	go wait.UntilWithContext(ctx, s.sync, 30*time.Second)
}

// sync refreshes the ShareGPU decisions by scanning machine state.
// Complexity: O(D + C) per invocation.
func (s *shareGPUManager) sync(ctx context.Context) {
	if s.basePlugin == nil {
		general.Infof("share gpu manager sync failed, basePlugin is nil")
		return
	}

	s.Lock()
	defer s.Unlock()

	baseState := s.basePlugin.GetState()
	if baseState == nil {
		return
	}

	machineState, ok := baseState.GetMachineState()[gpuconsts.GPUDeviceType]
	if !ok {
		general.Infof("share gpu manager found no GPU machine state; skipping")
		return
	}

	// Build a fresh snapshot with per-sync indicator cache.
	shareGPUMap := make(map[string]bool, len(machineState))
	indicatorCache := make(map[types.UID]bool)
	for id, alloc := range machineState {
		shareGPUMap[id] = s.evaluateDeviceShareStatus(ctx, alloc, indicatorCache)
	}

	if reflect.DeepEqual(s.shareGPUMap, shareGPUMap) {
		return
	}

	general.Infof("share gpu manager updated from: %v, to: %v", s.shareGPUMap, shareGPUMap)
	s.shareGPUMap = shareGPUMap
	s.basePlugin.TriggerReporter()
}

// evaluateDeviceShareStatus scans main containers for a device and returns true
// if and only if all of them enable ShareGPU. Any error retrieving indicators is
// treated as non-blocking and the container is ignored (optimistic sharing).
// evaluateDeviceShareStatus returns true iff all main containers enable ShareGPU.
// Complexity: O(Cd) where Cd is number of main containers on the device.
func (s *shareGPUManager) evaluateDeviceShareStatus(ctx context.Context, alloc *state.AllocationState, cache map[types.UID]bool) bool {
	if alloc == nil {
		return false
	}

	// Default to shareable and short-circuit to false once a disallowed pod is found.
	for _, containerEntries := range alloc.PodEntries {
		for _, container := range containerEntries {
			if !container.CheckMainContainer() || container.CheckReclaimed() {
				continue
			}

			enableShareGPU := s.evaluateContainerDeviceShareStatus(ctx, container, cache)
			if !enableShareGPU {
				return false
			}
		}
	}

	return true
}

// evaluateContainerDeviceShareStatus checks a single container's pod-level indicator.
// If a cache is provided, it memoizes decisions by pod UID.
// Complexity: O(1) with cache hit; O(ExternalCall) otherwise.
func (s *shareGPUManager) evaluateContainerDeviceShareStatus(ctx context.Context, container *state.AllocationInfo, cache map[types.UID]bool) bool {
	podMeta := s.preparePodMeta(container)

	if cache != nil {
		if v, ok := cache[podMeta.UID]; ok {
			return v
		}
	}

	enableShareGPU, err := s.getPodEnableShareGPU(ctx, podMeta)
	if err != nil {
		general.Infof("share gpu manager: fetching extended indicators failed for pod %s/%s: %v", podMeta.Namespace, podMeta.Name, err)
		if cache != nil {
			cache[podMeta.UID] = false
		}
		return false
	}

	if cache != nil {
		cache[podMeta.UID] = enableShareGPU
	}
	return enableShareGPU
}

// getPodEnableShareGPU queries MetaServer for the pod's `EnableShareGPU` indicator.
// Returns: boolean indicator value; error when external call fails.
// Errors: wrapped with context using `pkg/errors`.
func (s *shareGPUManager) getPodEnableShareGPU(ctx context.Context, podMeta metav1.ObjectMeta) (bool, error) {
	enableShareGPU := false
	indicators := v1alpha1.ReclaimResourceIndicators{}
	baseLine, err := s.basePlugin.MetaServer.ServiceExtendedIndicator(ctx, podMeta, &indicators)
	if err != nil {
		return false, errors.Wrapf(err, "ServiceExtendedIndicator failed for pod %s/%s", podMeta.Namespace, podMeta.Name)
	}

	if !baseLine && indicators.EnableShareGPU != nil {
		enableShareGPU = *indicators.EnableShareGPU
	}

	return enableShareGPU, nil
}

func (s *shareGPUManager) preparePodMeta(info *state.AllocationInfo) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		UID:         types.UID(info.PodUid),
		Namespace:   info.PodNamespace,
		Name:        info.PodName,
		Labels:      info.Labels,
		Annotations: info.Annotations,
	}
}
