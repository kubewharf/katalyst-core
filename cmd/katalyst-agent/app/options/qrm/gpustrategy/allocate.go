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

package gpustrategy

import (
	"strings"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/gpustrategy"
)

type AllocateStrategyOptions struct {
	CustomFilteringStrategies map[string]string
	CustomSortingStrategy     map[string]string
	CustomBindingStrategy     map[string]string
	CustomAllocationStrategy  map[string]string
}

func NewGPUAllocateStrategyOptions() *AllocateStrategyOptions {
	return &AllocateStrategyOptions{}
}

func (o *AllocateStrategyOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("allocate_strategy")
	fs.StringToStringVar(&o.CustomFilteringStrategies, "gpu-allocate-custom-filtering-strategies",
		o.CustomFilteringStrategies, "The filtering strategies for each resource, e.g. gpu:filtering1/filtering2")
	fs.StringToStringVar(&o.CustomSortingStrategy, "gpu-allocate-custom-sorting-strategy", o.CustomSortingStrategy, "The sorting strategy for each resource")
	fs.StringToStringVar(&o.CustomBindingStrategy, "gpu-allocate-custom-binding-strategy", o.CustomBindingStrategy, "The binding strategy for each resource")
	fs.StringToStringVar(&o.CustomAllocationStrategy, "gpu-allocate-custom-allocation-strategy", o.CustomAllocationStrategy, "The allocation strategy for each resource")
}

func (o *AllocateStrategyOptions) ApplyTo(c *gpustrategy.AllocateStrategyConfig) error {
	for resourceName, strategies := range o.CustomFilteringStrategies {
		filteringStrategies := strings.Split(strategies, "/")
		for _, strategyName := range filteringStrategies {
			c.CustomFilteringStrategies[resourceName] = append(c.CustomFilteringStrategies[resourceName], strategyName)
		}
	}

	c.CustomSortingStrategy = o.CustomSortingStrategy
	c.CustomBindingStrategy = o.CustomBindingStrategy
	c.CustomAllocationStrategy = o.CustomAllocationStrategy
	return nil
}
