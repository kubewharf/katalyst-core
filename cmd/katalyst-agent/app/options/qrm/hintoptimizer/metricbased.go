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

	"github.com/kubewharf/katalyst-core/pkg/config/agent/qrm/hintoptimizer"
)

type MetricBasedHintOptimizerOptions struct {
	EnableMetricPreferredNumaAllocation bool
}

func NewMetricBasedHintOptimizerOptions() *MetricBasedHintOptimizerOptions {
	return &MetricBasedHintOptimizerOptions{
		EnableMetricPreferredNumaAllocation: false,
	}
}

func (o *MetricBasedHintOptimizerOptions) AddFlags(fss *cliflag.NamedFlagSets) {
	fs := fss.FlagSet("metric_based_hint_optimizer")

	fs.BoolVar(&o.EnableMetricPreferredNumaAllocation, "enable-metric-preferred-numa-allocation", o.EnableMetricPreferredNumaAllocation,
		"if set true, we will enable metric preferred numa")
}

func (o *MetricBasedHintOptimizerOptions) ApplyTo(conf *hintoptimizer.MetricBasedHintOptimizerConfig) error {
	conf.EnableMetricPreferredNumaAllocation = o.EnableMetricPreferredNumaAllocation
	return nil
}
