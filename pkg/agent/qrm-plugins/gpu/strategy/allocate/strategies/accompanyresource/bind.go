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

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/state"
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

	enableHybridAccompanyResourceAllocation := ctx != nil &&
		ctx.GPUQRMPluginConfig != nil &&
		ctx.GPUQRMPluginConfig.EnableHybridAccompanyResourceAllocation

	// device allocation is shared when hybrid allocation is enabled and the pod has the shared annotation key.
	requestSharedDeviceAllocation := enableHybridAccompanyResourceAllocation &&
		ctx.ResourceReq != nil &&
		util.IsSharedDeviceAllocationRequested(
			ctx.ResourceReq.Annotations,
			ctx.GPUQRMPluginConfig.AccompanyResourceSharedAnnotationKey,
		)

	valid, errMsg := strategies.IsBindingContextValid(
		ctx,
		sortedDevices,
		requestSharedDeviceAllocation,
	)
	if !valid {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: errMsg,
		}, fmt.Errorf("%s", errMsg)
	}

	// Topology uses resource names, while MachineState uses resolved device types.
	accompanyResourceName := ctx.AccompanyResourceName
	if accompanyResourceName == "" {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: "no accompany resource name specified",
		}, fmt.Errorf("no accompany resource name specified")
	}

	resourceName := ctx.ResourceName

	// Reusable devices count toward the current request.
	allocatedDevices := sets.NewString(ctx.DeviceReq.ReusableDevices...)
	machineState := ctx.MachineState

	accompanyResourceNameInState := util.ResolveResourceName(ctx.DeviceNameToTypeMap, accompanyResourceName, false)
	if accompanyResourceNameInState == "" {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("invalid accompany resource %s in state", accompanyResourceName),
		}, fmt.Errorf("invalid accompany resource %s in state", accompanyResourceName)
	}

	accompanyAllocatedDeviceIDs := machineState.GetAllocatedDeviceIDs(v1.ResourceName(accompanyResourceNameInState), ctx.ResourceReq.PodUid, ctx.ResourceReq.ContainerName)
	if len(accompanyAllocatedDeviceIDs) == 0 {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("no accompany resource device found for pod %s, container %s, resource %s", ctx.ResourceReq.PodUid, ctx.ResourceReq.ContainerName, accompanyResourceNameInState),
		}, fmt.Errorf("no accompany resource device found for pod %s, container %s, resource %s", ctx.ResourceReq.PodUid, ctx.ResourceReq.ContainerName, accompanyResourceNameInState)
	}

	resourceNameInState := util.ResolveResourceName(ctx.DeviceNameToTypeMap, resourceName, false)
	if resourceNameInState == "" {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("invalid resource %s in state", resourceName),
		}, fmt.Errorf("invalid resource %s in state", resourceName)
	}

	accompanyResourceToDeviceRatio := machineState.GetRatioOfAccompanyResourceToTargetResource(accompanyResourceNameInState, resourceNameInState)
	devicesToAllocate, err := machineState.CalculateTargetDevicesToAllocate(
		accompanyResourceToDeviceRatio,
		len(accompanyAllocatedDeviceIDs),
		enableHybridAccompanyResourceAllocation,
	)
	if err != nil {
		return failedAllocationResult(fmt.Errorf("failed to calculate target devices to allocate: %w", err))
	}

	if allocatedDevices.Len() >= devicesToAllocate {
		return &allocate.AllocationResult{
			AllocatedDevices: allocatedDevices.UnsortedList(),
			Success:          true,
		}, nil
	}

	affinityDevices, err := ctx.DeviceTopologyRegistry.GetAffinityDevices(accompanyResourceName, resourceName)
	if err != nil {
		general.Warningf("failed to get affinity devices between %s and %s: %v", accompanyResourceName, resourceName, err)
		return nil, fmt.Errorf("failed to get affinity devices between %s and %s: %w", accompanyResourceName, resourceName, err)
	}

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

	if len(affinityDevices) == 0 {
		general.Warningf("no affinity devices between accompany resource %s and main resource %s, pod: %s/%s, container: %s",
			accompanyResourceName, ctx.ResourceName,
			ctx.ResourceReq.PodNamespace, ctx.ResourceReq.PodName, ctx.ResourceReq.ContainerName)
		emitAccompanyResourceAllocationMetric(ctx, metricNameNoAccompanyAffinityDevices, accompanyResourceName)

		return &allocate.AllocationResult{
			Success:          true,
			AllocatedDevices: []string{},
		}, nil
	}

	// Shared requests may reuse shared devices outside kubelet's available set.
	if requestSharedDeviceAllocation {
		return s.allocateSharedAffinityDevices(
			ctx,
			accompanyAllocatedDeviceIDs,
			affinityDevices,
			priorityDimensions,
			allocatedDevices,
			devicesToAllocate,
			resourceNameInState,
		)
	}

	// Exclusive requests allocate only from kubelet's available devices.
	return s.allocateExclusiveAffinityDevices(
		ctx,
		accompanyAllocatedDeviceIDs,
		affinityDevices,
		priorityDimensions,
		sortedDevices,
		allocatedDevices,
		devicesToAllocate,
		accompanyResourceToDeviceRatio,
		accompanyResourceName,
	)
}

// allocateExclusiveAffinityDevices is responsible for exclusive allocation for only currently available target devices.
// This strategy is not able to have pods share devices.
// For each accompany device, it searches affinity groups in priority order and applies
// a ratio-based limit to distribute target devices across the accompany devices.
func (s *AccompanyResourceStrategy) allocateExclusiveAffinityDevices(
	ctx *allocate.AllocationContext,
	allocatedAccompanyDeviceIDs []string,
	affinityByDevice map[string]map[string]machine.DeviceIDs,
	priorityDimensions []string,
	sortedDevices []string,
	selected sets.String,
	devicesToAllocate int,
	accompanyResourceToDeviceRatio float64,
	accompanyResourceName string,
) (*allocate.AllocationResult, error) {
	// sortedDevices contains devices that are not already allocated.
	available := sets.NewString(sortedDevices...)

	// The ratio determines how many target devices one accompany device may receive.
	// For example, a ratio of 0.5 permits two target devices per accompany device.
	perAccompanyLimit := 1
	if accompanyResourceToDeviceRatio > 0 {
		perAccompanyLimit = int(math.Max(1/accompanyResourceToDeviceRatio, 1))
	}

	// Allocate targets for each accompany device before moving to the next one.
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
		// Earlier dimensions represent stronger affinity.
		for _, dimName := range priorityDimensions {
			deviceIDs, ok := affinityForDevice[dimName]
			if !ok {
				continue
			}
			for _, deviceID := range deviceIDs {
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

				// Stop allocating for this accompany device once its proportional share is met.
				if allocatedForCurrent >= perAccompanyLimit {
					break PriorityLoop
				}
			}
		}
	}

	// Return the partial allocation and do not reject the pod.
	return partialAllocationResult(
		ctx,
		selected.UnsortedList(),
		devicesToAllocate,
		accompanyResourceName,
	)
}

// allocateSharedAffinityDevices reuses affinity devices owned only by shared pods
// before consuming free devices from kubelet's available set.
func (s *AccompanyResourceStrategy) allocateSharedAffinityDevices(
	ctx *allocate.AllocationContext,
	allocatedAccompanyResourceIDs []string,
	affinityByAccompanyResource map[string]map[string]machine.DeviceIDs,
	priorityDimensions []string,
	selected sets.String,
	devicesToAllocate int,
	resourceNameInState string,
) (*allocate.AllocationResult, error) {
	// Resolve the affinity devices selected for the allocated accompany devices.
	// This uses the same deterministic mapping exposed by topology reporting.
	affinityDeviceIDs, err := resolveAffinityDeviceIDsForAccompanyResources(
		allocatedAccompanyResourceIDs,
		affinityByAccompanyResource,
		priorityDimensions,
	)
	if err != nil {
		return failedAllocationResult(err)
	}

	// Existing devices that are allocated to other shared pods are preferred over free devices.
	// This is to maximize bin-packing and reduce resource fragmentation.
	candidates := getSharedResourceAllocationCandidates(
		affinityDeviceIDs,
		ctx.MachineState[v1.ResourceName(resourceNameInState)],
		sets.NewString(ctx.DeviceReq.AvailableDevices...),
		selected,
		ctx.GPUQRMPluginConfig.AccompanyResourceSharedAnnotationKey,
	)

	remaining := devicesToAllocate - selected.Len()
	// Preserve best-effort behavior when affinity or ownership constraints leave
	// fewer candidates than requested.
	if len(candidates) < remaining {
		selected.Insert(candidates...)
		return partialAllocationResult(
			ctx,
			selected.List(),
			devicesToAllocate,
			ctx.AccompanyResourceName,
		)
	}

	selected.Insert(candidates[:remaining]...)
	return &allocate.AllocationResult{
		AllocatedDevices: selected.List(),
		Success:          true,
	}, nil
}

// resolveAffinityDeviceIDsForAccompanyResources resolves one affinity device for each
// allocated accompany device using the configured priority dimensions.
// It rejects missing mappings and deduplicates devices shared by multiple accompany devices.
func resolveAffinityDeviceIDsForAccompanyResources(
	allocatedAccompanyResourceIDs []string,
	affinityByAccompanyResource map[string]map[string]machine.DeviceIDs,
	priorityDimensions []string,
) (sets.String, error) {
	targetDeviceByAccompanyResource := util.ResolveAffinityDeviceByAccompanyResource(
		affinityByAccompanyResource,
		priorityDimensions,
	)
	affinityDeviceIDs := sets.NewString()
	for _, accompanyResourceID := range allocatedAccompanyResourceIDs {
		resourceID, ok := targetDeviceByAccompanyResource[accompanyResourceID]
		if !ok {
			return nil, fmt.Errorf("no affinity device found for accompany resource device %s", accompanyResourceID)
		}
		affinityDeviceIDs.Insert(resourceID)
	}
	return affinityDeviceIDs, nil
}

// getSharedResourceAllocationCandidates returns shared devices before free devices.
// This is because shared devices are preferred over free devices.
// This is to maximize bin-packing and reduce resource fragmentation.
func getSharedResourceAllocationCandidates(
	eligible sets.String,
	resourceState state.AllocationMap,
	available sets.String,
	selected sets.String,
	annotationKey string,
) []string {
	shared := sets.NewString()
	free := sets.NewString()

	for _, resourceID := range eligible.List() {
		if selected.Has(resourceID) {
			continue
		}

		switch classifyResourceOwnership(resourceState[resourceID], annotationKey) {
		case resourceShared:
			shared.Insert(resourceID)
		case resourceFree:
			if available.Has(resourceID) {
				free.Insert(resourceID)
			}
		case resourceExclusive:
			// Never mix shared and exclusive owners on one device.
		}
	}

	return append(shared.List(), free.List()...)
}

type resourceOwnership int

const (
	resourceFree resourceOwnership = iota
	resourceShared
	resourceExclusive
)

// classifyResourceOwnership treats a device as shared only when every owner opted in.
// Any exclusive or unknown owner makes the whole device exclusive.
func classifyResourceOwnership(
	allocationState *state.AllocationState,
	annotationKey string,
) resourceOwnership {
	if allocationState == nil {
		return resourceFree
	}

	hasOwner := false
	for _, containerEntries := range allocationState.PodEntries {
		for _, allocationInfo := range containerEntries {
			hasOwner = true
			if allocationInfo == nil ||
				!util.IsSharedDeviceAllocationRequested(allocationInfo.Annotations, annotationKey) {
				return resourceExclusive
			}
		}
	}
	if !hasOwner {
		return resourceFree
	}
	return resourceShared
}

func failedAllocationResult(err error) (*allocate.AllocationResult, error) {
	return &allocate.AllocationResult{
		Success:      false,
		ErrorMessage: err.Error(),
	}, err
}

// partialAllocationResult reports insufficient capacity and returns the devices
// selected by either the exclusive or hybrid allocation model. A metric is also emitted.
func partialAllocationResult(
	ctx *allocate.AllocationContext,
	allocatedDevices []string,
	devicesToAllocate int,
	accompanyResourceName string,
) (*allocate.AllocationResult, error) {
	general.Warningf("not enough devices to allocate: need %d, have %d, pod: %s/%s, container: %s, resourceName: %s, accompanyResource: %s",
		devicesToAllocate, len(allocatedDevices),
		ctx.ResourceReq.PodNamespace, ctx.ResourceReq.PodName, ctx.ResourceReq.ContainerName,
		ctx.ResourceName, accompanyResourceName)
	emitAccompanyResourceAllocationMetric(ctx, metricNameNotEnoughAllocatedDevicesAccompanyBind, accompanyResourceName)

	return &allocate.AllocationResult{
		Success:          true,
		AllocatedDevices: allocatedDevices,
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
