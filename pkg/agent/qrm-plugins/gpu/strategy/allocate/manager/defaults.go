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
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/canonical"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/deviceaffinity"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/scheduler"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/gpu/strategy/allocate/strategies/virtual_gpu"
	"github.com/kubewharf/katalyst-core/pkg/util/general"
)

// registerDefaultFilterStrategies register filtering strategies
func registerDefaultFilterStrategies(manager *StrategyManager) {
	if err := manager.RegisterFilteringStrategy(virtual_gpu.NewVirtualGPUStrategy()); err != nil {
		general.Errorf("Failed to register filtering strategy: %v", err)
	}

	if err := manager.RegisterFilteringStrategy(canonical.NewCanonicalStrategy()); err != nil {
		general.Errorf("Failed to register sorting strategy: %v", err)
	}

	if err := manager.RegisterFilteringStrategy(scheduler.NewSchedulerStrategy()); err != nil {
		general.Errorf("Failed to register filtering strategy: %v", err)
	}
}

// registerDefaultSortingStrategies register sorting strategies
func registerDefaultSortingStrategies(manager *StrategyManager) {
	if err := manager.RegisterSortingStrategy(virtual_gpu.NewVirtualGPUStrategy()); err != nil {
		general.Errorf("Failed to register sorting strategy: %v", err)
	}
}

// registerDefaultBindingStrategies register binding strategies
func registerDefaultBindingStrategies(manager *StrategyManager) {
	if err := manager.RegisterBindingStrategy(canonical.NewCanonicalStrategy()); err != nil {
		general.Errorf("Failed to register binding strategy: %v", err)
	}

	if err := manager.RegisterBindingStrategy(deviceaffinity.NewDeviceAffinityStrategy()); err != nil {
		general.Errorf("Failed to register binding strategy: %v", err)
	}
}

// registerDefaultAllocationStrategies register allocation strategies
func registerDefaultAllocationStrategies(manager *StrategyManager) {
	if err := manager.RegisterGenericAllocationStrategy(allocationStrategyNameDefault,
		[]string{canonical.StrategyNameCanonical, virtual_gpu.StrategyNameVirtualGPU, scheduler.StrategyNameScheduler},
		virtual_gpu.StrategyNameVirtualGPU, canonical.StrategyNameCanonical); err != nil {
		general.Errorf("Failed to register default allocation strategy: %v", err)
	}
}

// registerDefaultStrategies registers the default strategies
func registerDefaultStrategies(manager *StrategyManager) {
	registerDefaultFilterStrategies(manager)
	registerDefaultSortingStrategies(manager)
	registerDefaultBindingStrategies(manager)
	registerDefaultAllocationStrategies(manager)
}
