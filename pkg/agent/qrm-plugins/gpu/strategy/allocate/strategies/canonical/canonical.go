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

package canonical

import (
	"fmt"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	StrategyNameCanonical = "canonical"
)

// CanonicalStrategy binds GPU devices to the allocation context
type CanonicalStrategy struct{}

// NewCanonicalStrategy creates a new default binding strategy
func NewCanonicalStrategy() *CanonicalStrategy {
	return &CanonicalStrategy{}
}

var (
	_ allocate.BindingStrategy   = &CanonicalStrategy{}
	_ allocate.FilteringStrategy = &CanonicalStrategy{}
)

// Name returns the name of the binding strategy
func (s *CanonicalStrategy) Name() string {
	return StrategyNameCanonical
}

// Bind binds the sorted GPU devices to the allocation context
// It creates allocation info for the selected devices
func (s *CanonicalStrategy) Bind(
	ctx *allocate.AllocationContext, sortedDevices []string,
) (*allocate.AllocationResult, error) {
	valid, errMsg := strategies.IsBindingContextValid(ctx, sortedDevices)
	if !valid {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: errMsg,
		}, fmt.Errorf(errMsg)
	}

	devicesToAllocate := int(ctx.DeviceReq.DeviceRequest)
	allocatedDevices := sets.NewString()
	allocateDevices := func(devices ...string) bool {
		for _, device := range devices {
			allocatedDevices.Insert(device)
			if devicesToAllocate == allocatedDevices.Len() {
				return true
			}
		}
		return false
	}

	// First try to bind reusable devices
	if allocateDevices(ctx.DeviceReq.ReusableDevices...) {
		return &allocate.AllocationResult{
			AllocatedDevices: allocatedDevices.UnsortedList(),
			Success:          true,
		}, nil
	}

	// Then try to bind devices from sorted list
	if allocateDevices(sortedDevices...) {
		general.InfoS("Successfully bound devices",
			"podNamespace", ctx.ResourceReq.PodNamespace,
			"podName", ctx.ResourceReq.PodName,
			"containerName", ctx.ResourceReq.ContainerName,
			"allocatedDevices", allocatedDevices.List())

		return &allocate.AllocationResult{
			AllocatedDevices: allocatedDevices.UnsortedList(),
			Success:          true,
		}, nil
	}

	return &allocate.AllocationResult{
		Success:      false,
		ErrorMessage: fmt.Sprintf("not enough devices: need %d, have %d", devicesToAllocate, len(sortedDevices)),
	}, fmt.Errorf("not enough devices: need %d, have %d", devicesToAllocate, len(sortedDevices))
}

// Filter filters the available devices based on whether they are already occupied.
// The assumption is that each device can only be allocated to one container at most.
// It only returns devices that are not occupied yet.
func (s *CanonicalStrategy) Filter(
	ctx *allocate.AllocationContext, allAvailableDevices []string,
) ([]string, error) {
	machineState, ok := ctx.MachineState[v1.ResourceName(ctx.DeviceReq.DeviceName)]
	if !ok {
		return nil, fmt.Errorf("machine state for %s is not available", ctx.DeviceReq.DeviceName)
	}

	filteredDevices := sets.NewString()
	for _, device := range allAvailableDevices {
		if machineState.IsRequestSatisfied(device, 1, 1) {
			filteredDevices.Insert(device)
		}
	}

	return filteredDevices.UnsortedList(), nil
}
