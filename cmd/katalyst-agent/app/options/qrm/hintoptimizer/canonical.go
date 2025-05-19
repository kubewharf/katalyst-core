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
	cliflag "k8s.io/component-base/cli/flag"

	cpuconsts "github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/cpu/consts"
	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/hintoptimizer"
)

type CanonicalHintOptimizerOptions struct {
	CPUNUMAHintPreferPolicy       string
	CPUNUMAHintPreferLowThreshold float64
}

func NewCanonicalHintOptimizerOptions() *CanonicalHintOptimizerOptions {
	return &CanonicalHintOptimizerOptions{
		CPUNUMAHintPreferPolicy: cpuconsts.CPUNUMAHintPreferPolicySpreading,
	}
}

func (o *CanonicalHintOptimizerOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("canonical_hint_optimizer")

	fs.StringVar(&o.CPUNUMAHintPreferPolicy, "cpu-numa-hint-prefer-policy", o.CPUNUMAHintPreferPolicy,
		"it decides hint preference calculation strategy")
	fs.Float64Var(&o.CPUNUMAHintPreferLowThreshold, "cpu-numa-hint-prefer-low-threshold", o.CPUNUMAHintPreferLowThreshold,
		"it indicates threshold to apply CPUNUMAHintPreferPolicy dynamically, and it's working when CPUNUMAHintPreferPolicy is set to dynamic_packing")
}

func (o *CanonicalHintOptimizerOptions) ApplyTo(conf *hintoptimizer.CanonicalHintOptimizerConfig) error {
	conf.CPUNUMAHintPreferPolicy = o.CPUNUMAHintPreferPolicy
	conf.CPUNUMAHintPreferLowThreshold = o.CPUNUMAHintPreferLowThreshold
	return nil
}
