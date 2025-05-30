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

package hintoptimizer

import (
	"fmt"

	cliflag "k8s.io/component-base/cli/flag"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy/canonical"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/policy/metricbased"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/dynamicpolicy/hintoptimizer/registry"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/hintoptimizer"
)

type HintOptimizerOptions struct {
	SharedCoresHintOptimizerPolicies    []string
	DedicatedCoresHintOptimizerPolicies []string

	*CanonicalHintOptimizerOptions
	*MemoryBandwidthHintOptimizerOptions
	*MetricBasedHintOptimizerOptions
}

func NewHintOptimizerOptions() *HintOptimizerOptions {
	return &HintOptimizerOptions{
		SharedCoresHintOptimizerPolicies:    []string{metricbased.HintOptimizerNameMetricBased, canonical.HintOptimizerNameCanonical},
		CanonicalHintOptimizerOptions:       NewCanonicalHintOptimizerOptions(),
		MemoryBandwidthHintOptimizerOptions: NewMemoryBandwidthHintOptimizerOptions(),
		MetricBasedHintOptimizerOptions:     NewMetricBasedHintOptimizerOptions(),
	}
}

func (o *HintOptimizerOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("hint_optimizer")

	fs.StringSliceVar(&o.SharedCoresHintOptimizerPolicies, "shared-cores-hint-optimizer-policies", o.SharedCoresHintOptimizerPolicies,
		"it indicates the hint optimizer policies for shared cores, the order of the policies is important, and the first policy will be applied first")
	fs.StringSliceVar(&o.DedicatedCoresHintOptimizerPolicies, "dedicated-cores-hint-optimizer-policies", o.DedicatedCoresHintOptimizerPolicies,
		"it indicates the hint optimizer policies for dedicated cores, the order of the policies is important, and the first policy will be applied first")

	o.CanonicalHintOptimizerOptions.AddFlags(fss)
	o.MemoryBandwidthHintOptimizerOptions.AddFlags(fss)
	o.MetricBasedHintOptimizerOptions.AddFlags(fss)
}

func (o *HintOptimizerOptions) ApplyTo(conf *hintoptimizer.HintOptimizerConfiguration) error {
	for _, policy := range o.SharedCoresHintOptimizerPolicies {
		if _, ok := registry.SharedCoresHintOptimizerRegistry[policy]; !ok {
			return fmt.Errorf("policy %s is not registered in the registry", policy)
		}
		conf.SharedCoresHintOptimizerPolicies = append(conf.SharedCoresHintOptimizerPolicies, policy)
	}
	for _, policy := range o.DedicatedCoresHintOptimizerPolicies {
		if _, ok := registry.DedicatedCoresHintOptimizerRegistry[policy]; !ok {
			return fmt.Errorf("policy %s is not registered in the registry", policy)
		}
		conf.DedicatedCoresHintOptimizerPolicies = append(conf.DedicatedCoresHintOptimizerPolicies, policy)
	}

	if err := o.CanonicalHintOptimizerOptions.ApplyTo(conf.CanonicalHintOptimizerConfig); err != nil {
		return err
	}
	if err := o.MemoryBandwidthHintOptimizerOptions.ApplyTo(conf.MemoryBandwidthHintOptimizerConfig); err != nil {
		return err
	}
	if err := o.MetricBasedHintOptimizerOptions.ApplyTo(conf.MetricBasedHintOptimizerConfig); err != nil {
		return err
	}
	return nil
}
