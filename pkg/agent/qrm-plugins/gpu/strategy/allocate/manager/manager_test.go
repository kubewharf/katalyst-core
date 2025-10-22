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

package manager

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/allocation"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/canonical"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/gpu_memory"
)

type dummyStrategy struct {
	name string
}

func (s *dummyStrategy) Name() string {
	return s.name
}

func (s *dummyStrategy) Filter(_ *allocate.AllocationContext, allAvailableDevices []string) ([]string, error) {
	return allAvailableDevices, nil
}

func (s *dummyStrategy) Sort(_ *allocate.AllocationContext, allAvailableDevices []string) ([]string, error) {
	return allAvailableDevices, nil
}

func (s *dummyStrategy) Bind(_ *allocate.AllocationContext, _ []string) (*allocate.AllocationResult, error) {
	return &allocate.AllocationResult{}, nil
}

func (s *dummyStrategy) Allocate(_ *allocate.AllocationContext) (*allocate.AllocationResult, error) {
	return &allocate.AllocationResult{}, nil
}

func TestStrategyRegistry(t *testing.T) {
	t.Parallel()

	registry := NewStrategyRegistry()
	// Test filtering strategy registration
	filteringStrategy := &dummyStrategy{name: "test-filtering"}
	err := registry.RegisterFilteringStrategy(filteringStrategy)
	assert.NoError(t, err)

	// Test duplicate registration
	err = registry.RegisterFilteringStrategy(filteringStrategy)
	assert.Error(t, err)

	// Test strategy retrieval
	retrievedStrategy, err := registry.GetFilteringStrategy("test-filtering")
	assert.NoError(t, err)
	assert.Equal(t, "test-filtering", retrievedStrategy.Name())

	// Test non-existent strategy
	_, err = registry.GetFilteringStrategy("non-existent")
	assert.Error(t, err)

	// Test sorting strategy registration
	sortingStrategy := &dummyStrategy{name: "test-sorting"}
	err = registry.RegisterSortingStrategy(sortingStrategy)
	assert.NoError(t, err)

	// Test binding strategy registration
	bindingStrategy := &dummyStrategy{name: "test-binding"}
	err = registry.RegisterBindingStrategy(bindingStrategy)
	assert.NoError(t, err)

	// Test allocation strategy registration
	err = registry.RegisterGenericAllocationStrategy("test-allocation", []string{"test-filtering"},
		"test-sorting", "test-binding")
	assert.NoError(t, err)

	// Test allocation strategy retrieval
	allocationStrategy, err := registry.GetAllocationStrategy("test-allocation")
	assert.NoError(t, err)
	assert.Equal(t, "test-filtering", allocationStrategy.(*allocation.GenericAllocationStrategy).FilteringStrategy[0].Name())
	assert.Equal(t, "test-sorting", allocationStrategy.(*allocation.GenericAllocationStrategy).SortingStrategy.Name())
	assert.Equal(t, "test-binding", allocationStrategy.(*allocation.GenericAllocationStrategy).BindingStrategy.Name())
}

func TestStrategyManager(t *testing.T) {
	t.Parallel()

	manager := NewStrategyManager()

	registerDefaultStrategies(manager)

	// Test default strategy
	assert.Equal(t, "default", manager.defaultStrategy)

	// Test setting default strategy
	err := manager.SetDefaultStrategy("test-allocation")
	assert.Error(t, err) // Should fail because test-allocation is not registered yet

	// Test registering strategy for resource
	err = manager.RegisterStrategyForResource("test-resource", "test-allocation")
	assert.Error(t, err) // Should fail because test-allocation is not registered yet

	err = manager.RegisterGenericAllocationStrategy("test-allocation", []string{gpu_memory.StrategyNameGPUMemory},
		gpu_memory.StrategyNameGPUMemory, canonical.StrategyNameCanonical)
	assert.NoError(t, err)

	// Now test registering strategy for resource
	err = manager.RegisterStrategyForResource("test-resource", "test-allocation")
	assert.NoError(t, err)

	// Test getting strategy for resource
	strategyName := manager.GetStrategyForResource("test-resource")
	assert.Equal(t, "test-allocation", strategyName)

	// Test getting default strategy for non-existent resource
	strategyName = manager.GetStrategyForResource("non-existent-resource")
	assert.Equal(t, "default", strategyName)
}
