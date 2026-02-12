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

import (
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/registry"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// GenericAllocationStrategy combines filtering, sorting, and binding strategies
type GenericAllocationStrategy struct {
	name                string
	registry            *registry.StrategyRegistry
	filteringStrategies []allocate.FilteringStrategy
	sortingStrategy     allocate.SortingStrategy
	bindingStrategy     allocate.BindingStrategy
}

// NewGenericAllocationStrategy creates a new allocation strategy with the given components
func NewGenericAllocationStrategy(name string,
	registry *registry.StrategyRegistry,
	filtering []allocate.FilteringStrategy,
	sorting allocate.SortingStrategy,
	binding allocate.BindingStrategy,
) *GenericAllocationStrategy {
	return &GenericAllocationStrategy{
		name:                name,
		registry:            registry,
		filteringStrategies: filtering,
		sortingStrategy:     sorting,
		bindingStrategy:     binding,
	}
}

var _ allocate.AllocationStrategy = &GenericAllocationStrategy{}

func (s *GenericAllocationStrategy) Name() string {
	return s.name
}

func (s *GenericAllocationStrategy) Clone(name string) *GenericAllocationStrategy {
	filteringStrategies := make([]allocate.FilteringStrategy, len(s.filteringStrategies))
	copy(filteringStrategies, s.filteringStrategies)
	return &GenericAllocationStrategy{
		name:                name,
		registry:            s.registry,
		filteringStrategies: filteringStrategies,
		sortingStrategy:     s.sortingStrategy,
		bindingStrategy:     s.bindingStrategy,
	}
}

// Allocate performs the allocation using the combined strategies
func (s *GenericAllocationStrategy) Allocate(ctx *allocate.AllocationContext) (*allocate.AllocationResult, error) {
	var err error
	resourceName := ctx.DeviceReq.DeviceName
	allAvailableDevices := append(ctx.DeviceReq.ReusableDevices, ctx.DeviceReq.AvailableDevices...)
	// Apply filtering strategy
	for _, fs := range s.getFilteringStrategies(ctx, resourceName) {
		allAvailableDevices, err = fs.Filter(ctx, allAvailableDevices)
		if err != nil {
			return &allocate.AllocationResult{
				Success:      false,
				ErrorMessage: err.Error(),
			}, err
		}
	}

	// Apply sorting strategy
	sortedDevices, err := s.getSortingStrategy(ctx, resourceName).Sort(ctx, allAvailableDevices)
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: err.Error(),
		}, err
	}

	// Apply binding strategy
	result, err := s.getBindingStrategy(ctx, resourceName).Bind(ctx, sortedDevices)
	if err != nil {
		return &allocate.AllocationResult{
			Success:      false,
			ErrorMessage: err.Error(),
		}, err
	}

	return result, nil
}

// GetFilteringStrategy returns the filtering strategy
func (s *GenericAllocationStrategy) GetFilteringStrategy() []allocate.FilteringStrategy {
	return s.filteringStrategies
}

// SetFilteringStrategy sets the filtering strategy
func (s *GenericAllocationStrategy) SetFilteringStrategy(filteringStrategies []allocate.FilteringStrategy) {
	s.filteringStrategies = filteringStrategies
}

// GetSortingStrategy returns the sorting strategy
func (s *GenericAllocationStrategy) GetSortingStrategy() allocate.SortingStrategy {
	return s.sortingStrategy
}

// SetSortingStrategy sets the sorting strategy
func (s *GenericAllocationStrategy) SetSortingStrategy(sortingStrategy allocate.SortingStrategy) {
	s.sortingStrategy = sortingStrategy
}

// GetBindingStrategy returns the binding strategy
func (s *GenericAllocationStrategy) GetBindingStrategy() allocate.BindingStrategy {
	return s.bindingStrategy
}

// SetBindingStrategy sets the binding strategy
func (s *GenericAllocationStrategy) SetBindingStrategy(bindingStrategy allocate.BindingStrategy) {
	s.bindingStrategy = bindingStrategy
}

func (s *GenericAllocationStrategy) getFilteringStrategies(ctx *allocate.AllocationContext, resourceName string) []allocate.FilteringStrategy {
	if strategyNames, ok := ctx.GPUQRMPluginConfig.CustomFilteringStrategies[resourceName]; ok {
		filteringStrategies := make([]allocate.FilteringStrategy, len(strategyNames))
		for _, fs := range strategyNames {
			fs, err := s.registry.GetFilteringStrategy(fs)
			if err != nil {
				general.Errorf("failed to get filtering strategy %s: %v", fs, err)
				continue
			}
			filteringStrategies = append(filteringStrategies, fs)
		}
		return filteringStrategies
	} else {
		return s.filteringStrategies
	}
}

func (s *GenericAllocationStrategy) getSortingStrategy(ctx *allocate.AllocationContext, resourceName string) allocate.SortingStrategy {
	if strategyName, ok := ctx.GPUQRMPluginConfig.CustomSortingStrategy[resourceName]; ok {
		sortingStrategy, err := s.registry.GetSortingStrategy(strategyName)
		if err != nil {
			general.Errorf("failed to get sorting strategy %s: %v", strategyName, err)
			sortingStrategy = s.sortingStrategy
		}
		return sortingStrategy
	} else {
		return s.sortingStrategy
	}
}

func (s *GenericAllocationStrategy) getBindingStrategy(ctx *allocate.AllocationContext, resourceName string) allocate.BindingStrategy {
	if strategyName, ok := ctx.GPUQRMPluginConfig.CustomBindingStrategy[resourceName]; ok {
		bindingStrategy, err := s.registry.GetBindingStrategy(strategyName)
		if err != nil {
			general.Errorf("failed to get binding strategy %s: %v", strategyName, err)
			bindingStrategy = s.bindingStrategy
		}
		return bindingStrategy
	} else {
		return s.bindingStrategy
	}
}
