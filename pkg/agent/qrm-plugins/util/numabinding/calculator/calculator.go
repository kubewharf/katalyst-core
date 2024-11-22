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
	"sync"

	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/allocation"
	"github.com/kubewharf/katalyst-core/pkg/agent/qrm-plugins/util/state"
	coreconfig "github.com/kubewharf/katalyst-core/pkg/config"
	"github.com/kubewharf/katalyst-core/pkg/metaserver"
	"github.com/kubewharf/katalyst-core/pkg/metrics"
	"github.com/kubewharf/katalyst-core/pkg/util/machine"
)

var (
	numaBindingCalculatorOnce sync.Once
	numaBindingCalculator     NUMABindingCalculator
)

type NUMABindingCalculator interface {
	Name() string
	Run(ctx context.Context)
	CalculateNUMABindingResult(current allocation.PodAllocations,
		numaAllocatable state.NUMAResource) (allocation.PodAllocations, bool, error)
}

func GetOrInitNUMABindingCalculator(
	conf *coreconfig.Configuration,
	emitter metrics.MetricEmitter,
	metaServer *metaserver.MetaServer,
	reservedCPUs machine.CPUSet,
) NUMABindingCalculator {
	numaBindingCalculatorOnce.Do(func() {
		calculators := make([]NUMABindingCalculator, 0)
		calculators = append(calculators, NewGreedyCalculator())
		calculators = append(calculators, NewBackTrackingCalculator(conf, emitter, metaServer, reservedCPUs))
		calculators = append(calculators, NewRandomCalculator(10000))
		numaBindingCalculator = NewDryRunCalculator(emitter, calculators...)
		numaBindingCalculator.Run(context.Background())
	})
	return numaBindingCalculator
}
