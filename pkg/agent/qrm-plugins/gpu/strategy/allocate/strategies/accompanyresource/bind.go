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

package accompanyresource

import (
	"fmt"
	"math"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/util"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

const (
	// metricNameNoAccompanyAffinityDevices is the metric name to record the number of
	// instances when the accompany strategy could not find any affinity devices between
	// the accompany resource and the main resource.
	metricNameNoAccompanyAffinityDevices = "no_accompany_affinity_devices"

	// metricNameNotEnoughAllocatedDevicesAccompanyBind is the metric name to record the number of
	// instances when the accompany strategy could not find enough affinity devices to satisfy
	// the requested allocation.
	metricNameNotEnoughAllocatedDevicesAccompanyBind = "not_enough_allocated_devices_accompany_bind"
)

// Bind tries to allocate devices by maximizing affinity with the accompany resource devices, making sure that it is
// allocated proportionally to the accompany resource.
func (s *AccompanyResourceStrategy) Bind(ctx *allocate.AllocationContext, sortedDevices []string) (*allocate.AllocationResult, error) {
	general.InfoS("accompany device strategy binding called",
		"available devices", sortedDevices)

	valid, errMsg := strategies.IsBindingContextValid(ctx, sortedDevices)
	if !valid {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: errMsg,
		}, fmt.Errorf("%s", errMsg)
	}

	// Two distinct identifiers are used throughout this strategy and must not be mixed:
	//   - device/resource name (e.g. "nvidia.com/gpu"): used by DeviceTopologyRegistry,
	//     which is keyed by the raw device resource name.
	//   - state name (e.g. "gpu"): used by MachineState, which is keyed by the device
	//     type. Resolve via util.ResolveResourceName before any state lookup.
	accompanyResourceName := ctx.AccompanyResourceName
	// AccompanyResource strategy requires an accompany resource name; without it, return an error.
	if accompanyResourceName == "" {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: "no accompany resource name specified",
		}, fmt.Errorf("no accompany resource name specified")
	}

	// resourceName is the raw target resource name (used for topology registry lookups).
	resourceName := ctx.ResourceName

	// Allocate all the reusable devices first
	allocatedDevices := sets.NewString(ctx.DeviceReq.ReusableDevices...)
	machineState := ctx.MachineState

	// Resolve resource name -> state name for any MachineState lookup below.
	accompanyResourceNameInState := util.ResolveResourceName(ctx.DeviceNameToTypeMap, accompanyResourceName, false)
	if accompanyResourceNameInState == "" {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("invalid accompany resource %s in state", accompanyResourceName),
		}, fmt.Errorf("invalid accompany resource %s in state", accompanyResourceName)
	}

	// MachineState lookup: use the state-name identifier.
	accompanyAllocatedDeviceIDs := machineState.GetAllocatedDeviceIDs(v1.ResourceName(accompanyResourceNameInState), ctx.ResourceReq.PodUid, ctx.ResourceReq.ContainerName)
	if len(accompanyAllocatedDeviceIDs) == 0 {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("no accompany resource device found for pod %s, container %s, resource %s", ctx.ResourceReq.PodUid, ctx.ResourceReq.ContainerName, accompanyResourceNameInState),
		}, fmt.Errorf("no accompany resource device found for pod %s, container %s, resource %s", ctx.ResourceReq.PodUid, ctx.ResourceReq.ContainerName, accompanyResourceNameInState)
	}

	// Resolve resource name -> state name for the target resource.
	resourceNameInState := util.ResolveResourceName(ctx.DeviceNameToTypeMap, resourceName, false)
	if resourceNameInState == "" {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("invalid resource %s in state", resourceName),
		}, fmt.Errorf("invalid resource %s in state", resourceName)
	}

	// MachineState lookup: pass state-name identifiers.
	accompanyResourceToDeviceRatio := machineState.GetRatioOfAccompanyResourceToTargetResource(accompanyResourceNameInState, resourceNameInState)

	// Find out the number of target devices to be allocated proportionally to the accompany resource devices
	devicesToBeAllocated, err := machineState.CalculateTargetDevicesToAllocate(accompanyResourceToDeviceRatio, len(accompanyAllocatedDeviceIDs))
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: err.Error(),
		}, fmt.Errorf("failed to calculate target devices to allocate: %w", err)
	}

	if allocatedDevices.Len() >= devicesToBeAllocated {
		return &allocate.AllocationResult{
			AllocatedDevices: allocatedDevices.UnsortedList(),
			Success:          true,
		}, nil
	}

	// DeviceTopologyRegistry lookup: pass the raw resource names (not the state names).
	affinityDevices, err := ctx.DeviceTopologyRegistry.GetAffinityDevices(accompanyResourceName, resourceName)
	if err != nil {
		general.Warningf("failed to get affinity devices between %s and %s: %v", accompanyResourceName, resourceName, err)
		return nil, fmt.Errorf("failed to get affinity devices between %s and %s: %w", accompanyResourceName, resourceName, err)
	}

	// Choose allocation path based on presence of affinity info
	// If there are no affinity devices, log a warning, emit a metric for visibility, and return
	// success with an empty allocation. This lets the pod proceed to be allocated (e.g. without RDMA devices in this
	// scenario) instead of being rejected.
	if len(affinityDevices) == 0 {
		general.Warningf("no affinity devices between accompany resource %s and main resource %s, pod: %s/%s, container: %s",
			accompanyResourceName, resourceName,
			ctx.ResourceReq.PodNamespace, ctx.ResourceReq.PodName, ctx.ResourceReq.ContainerName)
		emitAccompanyResourceAllocationMetric(ctx, metricNameNoAccompanyAffinityDevices, accompanyResourceName)

		return &allocate.AllocationResult{
			Success:          true,
			AllocatedDevices: []string{},
		}, nil
	}

	// DeviceTopologyRegistry lookup: pass the raw resource name (not the state name).
	var priorityDimensions []string
	accompanyTopology, err := ctx.DeviceTopologyRegistry.GetDeviceTopology(accompanyResourceName)
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("failed to get device topology for accompany resource %s: %v", accompanyResourceName, err),
		}, fmt.Errorf("failed to get device topology for accompany resource %s: %w", accompanyResourceName, err)
	}
	if accompanyTopology != nil {
		priorityDimensions = accompanyTopology.PriorityDimensions
	}

	// If no priority dimensions are specified from accompany device, use NUMA as the default priority dimension.
	if len(priorityDimensions) == 0 {
		priorityDimensions = []string{machine.DimensionNuma}
	}

	general.Infof("Get affinity devices from allocated devices: %v", affinityDevices)

	availableDevices := sets.NewString(sortedDevices...)

	// maxAllocationPerDevice is the maximum number of target devices that can be allocated to each accompany resource device.
	// This is to uniformly distribute the target devices to each accompany resource device.
	maxAllocationPerDevice := 1
	if accompanyResourceToDeviceRatio > 0 {
		maxAllocationPerDevice = int(math.Max(1/accompanyResourceToDeviceRatio, 1))
	}
	return s.allocateWithAffinity(ctx, accompanyAllocatedDeviceIDs, affinityDevices, priorityDimensions, availableDevices, allocatedDevices, devicesToBeAllocated, maxAllocationPerDevice, accompanyResourceName)
}

// allocateWithAffinity tries to allocate devices that have affinity with the allocated accompany devices.
func (s *AccompanyResourceStrategy) allocateWithAffinity(
	ctx *allocate.AllocationContext,
	allocatedAccompanyDeviceIDs []string,
	affinityByDevice map[string]map[string]machine.DeviceIDs,
	priorityDimensions []string,
	available sets.String,
	selected sets.String,
	devicesToAllocate int,
	perAccompanyLimit int,
	accompanyResourceName string,
) (*allocate.AllocationResult, error) {
	// Iterate each accompany resource device and bind target devices that have affinity with it.
	for _, accompanyID := range allocatedAccompanyDeviceIDs {
		var allocatedForCurrent int
		affinityForDevice, ok := affinityByDevice[accompanyID]
		if !ok {
			return &allocate.AllocationResult{
				Success:      false,
				ErrorMessage: fmt.Sprintf("accompany resource not found %s in affinity devices map, accompanyDeviceId: %s", accompanyResourceName, accompanyID),
			}, fmt.Errorf("accompany resource not found %s in affinity devices map, accompanyDeviceId: %s", accompanyResourceName, accompanyID)
		}

	PriorityLoop:
		for _, dimName := range priorityDimensions {
			deviceIDs, ok := affinityForDevice[dimName]
			if !ok {
				continue
			}
			for _, deviceID := range deviceIDs {
				// Skip devices that are not available or already selected
				if !available.Has(deviceID) || selected.Has(deviceID) {
					continue
				}

				allocatedForCurrent++
				selected.Insert(deviceID)
				if selected.Len() >= devicesToAllocate {
					general.InfoS("Successfully bound devices",
						"podNamespace", ctx.ResourceReq.PodNamespace,
						"podName", ctx.ResourceReq.PodName,
						"containerName", ctx.ResourceReq.ContainerName,
						"allocatedDevices", selected.List())

					return &allocate.AllocationResult{AllocatedDevices: selected.UnsortedList(), Success: true}, nil
				}

				// Move to the next accompany device once the perAccompanyLimit threshold is reached.
				if allocatedForCurrent >= perAccompanyLimit {
					break PriorityLoop
				}
			}
		}
	}

	// Not enough affinity devices are currently available to satisfy the proportional accompany allocation.
	// Rather than failing pod admission, we log a warning, emit a metric for visibility, and return
	// success with a partial allocation. This lets the pod proceed to be allocated (e.g. without the full expected number of RDMA devices)
	// instead of being rejected.
	general.Warningf("not enough devices to allocate: need %d, have %d, pod: %s/%s, container: %s, resourceName: %s, accompanyResource: %s",
		devicesToAllocate, len(selected),
		ctx.ResourceReq.PodNamespace, ctx.ResourceReq.PodName, ctx.ResourceReq.ContainerName,
		ctx.ResourceName, accompanyResourceName)
	emitAccompanyResourceAllocationMetric(ctx, metricNameNotEnoughAllocatedDevicesAccompanyBind, accompanyResourceName)

	return &allocate.AllocationResult{
		Success:          true,
		AllocatedDevices: selected.UnsortedList(),
	}, nil
}

// emitAccompanyResourceAllocationMetric emits a raw metric tagged with the pod / container /
// resource information that is common across the various accompany allocation failure
// scenarios.
func emitAccompanyResourceAllocationMetric(ctx *allocate.AllocationContext, metricName string, accompanyResourceName string) {
	tags := metrics.ConvertMapToTags(map[string]string{
		"podNamespace":          ctx.ResourceReq.PodNamespace,
		"podName":               ctx.ResourceReq.PodName,
		"containerName":         ctx.ResourceReq.ContainerName,
		"resourceName":          ctx.ResourceName,
		"accompanyResourceName": accompanyResourceName,
	})
	_ = ctx.Emitter.StoreInt64(metricName, 1, metrics.MetricTypeNameRaw, tags...)
}
