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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate"
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

	// Test filtering strategy retrieval
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

	// Test strategy retrieval
	retrievedSortingStrategy, err := registry.GetSortingStrategy("test-sorting")
	assert.NoError(t, err)
	assert.Equal(t, "test-sorting", retrievedSortingStrategy.Name())

	// Test binding strategy registration
	bindingStrategy := &dummyStrategy{name: "test-binding"}
	err = registry.RegisterBindingStrategy(bindingStrategy)
	assert.NoError(t, err)

	// Test allocation strategy registration
	allocatingStrategy := &dummyStrategy{name: "test-allocation"}
	err = registry.RegisterAllocationStrategy(allocatingStrategy)
	assert.NoError(t, err)

	// Test allocation strategy retrieval
	allocationStrategy, err := registry.GetAllocationStrategy("test-allocation")
	assert.NoError(t, err)
	assert.Equal(t, "test-allocation", allocationStrategy.Name())
}
