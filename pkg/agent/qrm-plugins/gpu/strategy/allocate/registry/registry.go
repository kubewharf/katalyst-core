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

package registry

import (
	"fmt"
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

type StrategyRegistry struct {
	filteringStrategies  map[string]allocate.FilteringStrategy
	sortingStrategies    map[string]allocate.SortingStrategy
	bindingStrategies    map[string]allocate.BindingStrategy
	allocationStrategies map[string]allocate.AllocationStrategy
	// Mutex for thread-safe access to registries
	registryMutex sync.RWMutex
}

// NewStrategyRegistry creates a new instance of StrategyRegistry
func NewStrategyRegistry() *StrategyRegistry {
	return &StrategyRegistry{
		filteringStrategies:  make(map[string]allocate.FilteringStrategy),
		sortingStrategies:    make(map[string]allocate.SortingStrategy),
		bindingStrategies:    make(map[string]allocate.BindingStrategy),
		allocationStrategies: make(map[string]allocate.AllocationStrategy),
	}
}

// RegisterFilteringStrategy registers a filtering strategy with the given name
func (r *StrategyRegistry) RegisterFilteringStrategy(strategy allocate.FilteringStrategy) error {
	r.registryMutex.Lock()
	defer r.registryMutex.Unlock()

	if _, exists := r.filteringStrategies[strategy.Name()]; exists {
		return fmt.Errorf("filtering strategy with name %s already registered", strategy.Name())
	}

	r.filteringStrategies[strategy.Name()] = strategy
	general.Infof("Registered filtering strategy: %s", strategy.Name())
	return nil
}

// RegisterSortingStrategy registers a sorting strategy with the given name
func (r *StrategyRegistry) RegisterSortingStrategy(strategy allocate.SortingStrategy) error {
	r.registryMutex.Lock()
	defer r.registryMutex.Unlock()

	if _, exists := r.sortingStrategies[strategy.Name()]; exists {
		return fmt.Errorf("sorting strategy with name %s already registered", strategy.Name())
	}

	r.sortingStrategies[strategy.Name()] = strategy
	general.Infof("Registered sorting strategy: %s", strategy.Name())
	return nil
}

// RegisterBindingStrategy registers a binding strategy with the given name
func (r *StrategyRegistry) RegisterBindingStrategy(strategy allocate.BindingStrategy) error {
	r.registryMutex.Lock()
	defer r.registryMutex.Unlock()

	if _, exists := r.bindingStrategies[strategy.Name()]; exists {
		return fmt.Errorf("binding strategy with name %s already registered", strategy.Name())
	}

	r.bindingStrategies[strategy.Name()] = strategy
	general.Infof("Registered binding strategy: %s", strategy.Name())
	return nil
}

func (r *StrategyRegistry) RegisterAllocationStrategy(strategy allocate.AllocationStrategy) error {
	r.registryMutex.Lock()
	defer r.registryMutex.Unlock()

	if _, exists := r.allocationStrategies[strategy.Name()]; exists {
		return fmt.Errorf("allocation strategy with name %s already registered", strategy.Name())
	}

	r.allocationStrategies[strategy.Name()] = strategy
	general.Infof("Registered allocation strategy: %s", strategy.Name())
	return nil
}

// GetFilteringStrategy returns the filtering strategy with the given name
func (r *StrategyRegistry) GetFilteringStrategy(name string) (allocate.FilteringStrategy, error) {
	r.registryMutex.RLock()
	defer r.registryMutex.RUnlock()

	strategy, exists := r.filteringStrategies[name]
	if !exists {
		return nil, fmt.Errorf("filtering strategy %s not found", name)
	}

	return strategy, nil
}

// GetSortingStrategy returns the sorting strategy with the given name
func (r *StrategyRegistry) GetSortingStrategy(name string) (allocate.SortingStrategy, error) {
	r.registryMutex.RLock()
	defer r.registryMutex.RUnlock()

	strategy, exists := r.sortingStrategies[name]
	if !exists {
		return nil, fmt.Errorf("sorting strategy %s not found", name)
	}

	return strategy, nil
}

// GetBindingStrategy returns the binding strategy with the given name
func (r *StrategyRegistry) GetBindingStrategy(name string) (allocate.BindingStrategy, error) {
	r.registryMutex.RLock()
	defer r.registryMutex.RUnlock()

	strategy, exists := r.bindingStrategies[name]
	if !exists {
		return nil, fmt.Errorf("binding strategy %s not found", name)
	}

	return strategy, nil
}

// GetAllocationStrategy returns the allocation strategy with the given name
func (r *StrategyRegistry) GetAllocationStrategy(name string) (allocate.AllocationStrategy, error) {
	r.registryMutex.RLock()
	defer r.registryMutex.RUnlock()

	strategy, exists := r.allocationStrategies[name]
	if !exists {
		return nil, fmt.Errorf("allocation strategy %s not found", name)
	}

	return strategy, nil
}
