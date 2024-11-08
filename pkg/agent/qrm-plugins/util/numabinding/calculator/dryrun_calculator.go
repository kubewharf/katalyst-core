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

package calculator

import (
	"context"

	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/general"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/allocation"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/state"
)

const (
	CalculatorNameDryRun = "dryRun"
)

type dryRunCalculator struct {
	calculators []NUMABindingCalculator
	emitter     metrics.MetricEmitter
}

func NewDryRunCalculator(emitter metrics.MetricEmitter, calculators ...NUMABindingCalculator) NUMABindingCalculator {
	withLoggingCalculators := make([]NUMABindingCalculator, 0, len(calculators))
	for _, calculator := range calculators {
		withLoggingCalculators = append(withLoggingCalculators, WithExecutionTimeLogging(calculator, emitter))
	}
	return &dryRunCalculator{
		calculators: withLoggingCalculators,
		emitter:     emitter,
	}
}

func (d *dryRunCalculator) Run(ctx context.Context) {
	for _, calc := range d.calculators {
		go calc.Run(ctx)
	}
}

func (d *dryRunCalculator) CalculateNUMABindingResult(current allocation.PodAllocations, numaAllocatable state.NUMAResource) (allocation.PodAllocations, bool, error) {
	for _, calc := range d.calculators {
		result, success, err := calc.CalculateNUMABindingResult(current, numaAllocatable)
		general.Infof("dry run calculator %s result: %v, success: %v, err: %v", calc.Name(), result, success, err)
		CheckAllNUMABindingResult(d.emitter, calc.Name(), success, result)
	}
	return current, true, nil
}

func (d *dryRunCalculator) Name() string {
	return CalculatorNameDryRun
}
