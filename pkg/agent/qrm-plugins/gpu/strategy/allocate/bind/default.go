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

package bind

import (
	"fmt"

	"k8s.io/apimachinery/pkg/util/sets"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

const (
	BindingStrategyNameDefault = "default"
)

// DefaultBindingStrategy binds GPU devices to the allocation context
type DefaultBindingStrategy struct{}

// NewDefaultBindingStrategy creates a new default binding strategy
func NewDefaultBindingStrategy() *DefaultBindingStrategy {
	return &DefaultBindingStrategy{}
}

// Name returns the name of the binding strategy
func (s *DefaultBindingStrategy) Name() string {
	return BindingStrategyNameDefault
}

// Bind binds the sorted GPU devices to the allocation context
// It creates allocation info for the selected devices
func (s *DefaultBindingStrategy) Bind(ctx *allocate.AllocationContext, sortedDevices []string) (*allocate.AllocationResult, error) {
	if ctx.GPUTopology == nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: "GPU topology is nil",
		}, fmt.Errorf("GPU topology is nil")
	}

	if len(sortedDevices) == 0 && ctx.DeviceReq.DeviceRequest > 0 {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: "no devices to bind",
		}, fmt.Errorf("no devices to bind")
	}

	// Determine how many devices to allocate
	devicesToAllocate := int(ctx.DeviceReq.DeviceRequest)
	if devicesToAllocate > len(sortedDevices) {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: fmt.Sprintf("not enough devices: need %d, have %d", devicesToAllocate, len(sortedDevices)),
		}, fmt.Errorf("not enough devices: need %d, have %d", devicesToAllocate, len(sortedDevices))
	}

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
