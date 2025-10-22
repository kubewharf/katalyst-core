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

package allocation

import "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"

// GenericAllocationStrategy combines filtering, sorting, and binding strategies
type GenericAllocationStrategy struct {
	name              string
	FilteringStrategy []allocate.FilteringStrategy
	SortingStrategy   allocate.SortingStrategy
	BindingStrategy   allocate.BindingStrategy
}

// NewGenericAllocationStrategy creates a new allocation strategy with the given components
func NewGenericAllocationStrategy(name string,
	filtering []allocate.FilteringStrategy,
	sorting allocate.SortingStrategy,
	binding allocate.BindingStrategy,
) *GenericAllocationStrategy {
	return &GenericAllocationStrategy{
		name:              name,
		FilteringStrategy: filtering,
		SortingStrategy:   sorting,
		BindingStrategy:   binding,
	}
}

var _ allocate.AllocationStrategy = &GenericAllocationStrategy{}

func (s *GenericAllocationStrategy) Name() string {
	return s.name
}

// Allocate performs the allocation using the combined strategies
func (s *GenericAllocationStrategy) Allocate(ctx *allocate.AllocationContext) (*allocate.AllocationResult, error) {
	var err error
	allAvailableDevices := append(ctx.DeviceReq.ReusableDevices, ctx.DeviceReq.AvailableDevices...)
	// Apply filtering strategy
	for _, fs := range s.FilteringStrategy {
		allAvailableDevices, err = fs.Filter(ctx, allAvailableDevices)
		if err != nil {
			return &allocate.AllocationResult{
				Success:      false,
				ErrorMessage: err.Error(),
			}, err
		}
	}

	// Apply sorting strategy
	sortedDevices, err := s.SortingStrategy.Sort(ctx, allAvailableDevices)
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: err.Error(),
		}, err
	}

	// Apply binding strategy
	result, err := s.BindingStrategy.Bind(ctx, sortedDevices)
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: err.Error(),
		}, err
	}

	return result, nil
}
